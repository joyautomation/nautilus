// Server.cs: HTTP, the token and the envelope (siemens-ide.md section 3.2).
//
// Every reply is {ok, data, events}; a failure carries {kind, message,
// fatal}. The one exception is a file read, which answers raw bytes.

using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.IO;
using System.Net;
using System.Net.Sockets;
using System.Text;
using System.Threading;

namespace Tiad
{
    // Handler answers one call: the reply's data, or Bytes for raw bytes.
    public delegate object Handler(Call c);

    // FileReply is a reply that is a file's raw bytes, not an envelope.
    // The file is streamed, never read whole into memory.
    public sealed class FileReply
    {
        public readonly string Path;

        public FileReply(string path)
        {
            Path = path;
        }
    }

    // Call is one request, routed.
    public sealed class Call
    {
        public readonly HttpListenerRequest Request;
        readonly Dictionary<string, string> vars;

        internal Call(HttpListenerRequest r, Dictionary<string, string> vars)
        {
            Request = r;
            this.vars = vars;
        }

        // Var is a {name} or {name...} part of the route, unescaped.
        public string Var(string name)
        {
            string v;
            return vars.TryGetValue(name, out v) ? v : "";
        }

        public Stream Body
        {
            get { return Request.InputStream; }
        }

        // MaxJsonBody caps a JSON request body.
        public const int MaxJsonBody = 16 << 20;

        // RequireContentType refuses a body of any other media type (415).
        // A browser can send a cross-origin text/plain POST without asking
        // first, and tiad never takes one.
        public void RequireContentType(string mediaType)
        {
            string ct = Request.ContentType ?? "";
            int semi = ct.IndexOf(';');
            if (semi >= 0) ct = ct.Substring(0, semi);
            if (!string.Equals(ct.Trim(), mediaType, StringComparison.OrdinalIgnoreCase))
            {
                throw new TiadException(415, Kinds.BadRequest, "Content-Type must be " + mediaType);
            }
        }

        // Json reads the body as one JSON object, strictly (see Reader).
        public Reader Json()
        {
            RequireContentType("application/json");
            var buf = new MemoryStream();
            var chunk = new byte[65536];
            int n;
            while ((n = Request.InputStream.Read(chunk, 0, chunk.Length)) > 0)
            {
                if (buf.Length + n > MaxJsonBody)
                {
                    throw new TiadException(413, Kinds.BadRequest, "body: larger than " + MaxJsonBody + " bytes");
                }
                buf.Write(chunk, 0, n);
            }
            string text = new UTF8Encoding(false).GetString(buf.GetBuffer(), 0, (int)buf.Length);
            if (text.Trim() == "") throw TiadException.BadRequest("body: EOF");
            return Reader.Root(Tiad.Json.Parse(text));
        }
    }

    public sealed class Router
    {
        sealed class Route
        {
            public string Method;
            public string Text;
            public string[] Pattern;
            public Handler Handler;
        }

        readonly List<Route> routes = new List<Route>();

        // Matched, when set, runs for every request a route takes, before
        // its handler, with the route ("POST /v1/sessions/{id}/compare")
        // and the request's route variables. The agent marks a session
        // touched here (UntouchedRule), so a route added later touches it
        // without anyone remembering to.
        // It is set once: a second assignment would silently drop the
        // agent's touch, so it throws.
        public Action<string, Dictionary<string, string>> Matched
        {
            get { return matched; }
            set
            {
                if (matched != null) throw new InvalidOperationException("Router.Matched is set already");
                matched = value;
            }
        }

        Action<string, Dictionary<string, string>> matched;

        // Add registers a handler. A pattern is "/v1/a/{name}/b" (one
        // segment) or "/v1/files/{path...}" (the rest, possibly empty).
        public void Add(string method, string pattern, Handler h)
        {
            routes.Add(new Route { Method = method, Text = method + " " + pattern, Pattern = pattern.TrimStart('/').Split('/'), Handler = h });
        }

        // Routes is every route registered, "METHOD /pattern", in order.
        public List<string> Routes()
        {
            var l = new List<string>();
            foreach (var r in routes) l.Add(r.Text);
            return l;
        }

        public Handler Match(string method, List<string> segs, out Dictionary<string, string> vars, out bool pathKnown)
        {
            pathKnown = false;
            foreach (var r in routes)
            {
                var v = new Dictionary<string, string>(StringComparer.Ordinal);
                if (!Fits(r.Pattern, segs, v)) continue;
                pathKnown = true;
                if (r.Method != method) continue;
                vars = v;
                if (Matched != null) Matched(r.Text, v);
                return r.Handler;
            }
            vars = null;
            return null;
        }

        static bool Fits(string[] pat, List<string> segs, Dictionary<string, string> v)
        {
            for (int i = 0; i < pat.Length; i++)
            {
                string p = pat[i];
                if (p.StartsWith("{", StringComparison.Ordinal) && p.EndsWith("...}", StringComparison.Ordinal))
                {
                    if (i != pat.Length - 1 || segs.Count < i) return false;
                    v[p.Substring(1, p.Length - 5)] = string.Join("/", segs.GetRange(i, segs.Count - i));
                    return true;
                }
                if (i >= segs.Count) return false;
                if (p.StartsWith("{", StringComparison.Ordinal))
                {
                    if (segs[i] == "") return false;
                    v[p.Substring(1, p.Length - 2)] = segs[i];
                }
                else if (p != segs[i])
                {
                    return false;
                }
            }
            return pat.Length == segs.Count;
        }
    }

    public static class Log
    {
        static readonly object mu = new object();

        // Line writes one log line to stderr. Never pass it a request body
        // or a secret.
        public static void Line(string format, params object[] args)
        {
            lock (mu)
            {
                Console.Error.WriteLine(DateTime.UtcNow.ToString("yyyy-MM-ddTHH:mm:ss.fffZ") + " " + string.Format(format, args));
            }
        }
    }

    public sealed class Server
    {
        readonly Config cfg;
        readonly Router router;
        readonly byte[] want;
        HttpListener listener;

        // Url is where the server listens, once Start has returned.
        public string Url;

        public Server(Config cfg, Router router)
        {
            this.cfg = cfg;
            this.router = router;
            if (cfg.Token == "") throw new ConfigException("a token is required");
            want = Encoding.UTF8.GetBytes("Bearer " + cfg.Token);
        }

        // Start binds the listener. Port 0 picks a free loopback port; a
        // test reads the port from the line Start prints:
        //   tiad: listening on http://127.0.0.1:NNNNN
        public void Start()
        {
            for (int attempt = 0; ; attempt++)
            {
                int port = cfg.Port;
                if (port == 0)
                {
                    var probe = new TcpListener(IPAddress.Loopback, 0);
                    probe.Start();
                    port = ((IPEndPoint)probe.LocalEndpoint).Port;
                    probe.Stop();
                }
                string host = cfg.Host;
                var l = new HttpListener();
                l.Prefixes.Add("http://" + host + ":" + port + "/");
                try
                {
                    l.Start();
                }
                catch (Exception)
                {
                    // Someone took the free port between the probe and the
                    // bind. Only a chosen port gets another try.
                    if (cfg.Port != 0 || attempt >= 20) throw;
                    continue;
                }
                listener = l;
                Url = "http://" + host + ":" + port;
                Console.Out.WriteLine("tiad: listening on " + Url);
                Console.Out.Flush();
                return;
            }
        }

        // Run serves requests until the process ends, each on a pool thread.
        public void Run()
        {
            while (true)
            {
                HttpListenerContext ctx = listener.GetContext();
                ThreadPool.QueueUserWorkItem(delegate { Handle(ctx); });
            }
        }

        bool Authorized(HttpListenerRequest r)
        {
            byte[] got = Encoding.UTF8.GetBytes(r.Headers["Authorization"] ?? "");
            // Constant time in the token's length.
            int diff = got.Length ^ want.Length;
            for (int i = 0; i < want.Length; i++)
            {
                diff |= want[i] ^ (i < got.Length ? got[i] : 0);
            }
            return diff == 0;
        }

        // Segments splits the raw request path and unescapes each segment,
        // so an escaped '/' never splits one.
        static List<string> Segments(string rawUrl)
        {
            string p = rawUrl;
            int q = p.IndexOfAny(new[] { '?', '#' });
            if (q >= 0) p = p.Substring(0, q);
            var segs = new List<string>(p.TrimStart('/').Split('/'));
            for (int i = 0; i < segs.Count; i++)
            {
                segs[i] = Uri.UnescapeDataString(segs[i]);
            }
            return segs;
        }

        void Handle(HttpListenerContext ctx)
        {
            var sw = Stopwatch.StartNew();
            var req = ctx.Request;
            int status = 200;
            string kind = "";
            try
            {
                // No browser ever calls tiad. A page the operator happens
                // to have open must not reach it, token or not.
                if (req.Headers["Origin"] != null)
                {
                    throw new TiadException(403, Kinds.Unauthorized, "a request with an Origin header (a browser) is refused");
                }
                if (!Authorized(req))
                {
                    throw new TiadException(401, Kinds.Unauthorized, "missing or wrong token");
                }
                Dictionary<string, string> vars;
                bool known;
                Handler h = router.Match(req.HttpMethod, Segments(req.RawUrl), out vars, out known);
                if (h == null)
                {
                    if (known) throw new TiadException(405, Kinds.BadRequest, req.HttpMethod + " is not allowed here");
                    throw TiadException.NotFound("no such call {0} {1}", req.HttpMethod, Path0(req.RawUrl));
                }
                object data = h(new Call(req, vars));
                var file = data as FileReply;
                if (file != null)
                {
                    WriteFile(ctx, file.Path);
                }
                else
                {
                    var env = new Obj();
                    env["ok"] = true;
                    if (data != null) env["data"] = data;
                    env["events"] = new object[0];
                    WriteEnvelope(ctx, 200, env);
                }
            }
            catch (TiadException e)
            {
                status = e.Status;
                kind = e.Kind;
                WriteEnvelope(ctx, e.Status, Failure(e.Kind, e.Message, e.Fatal, e.Payload));
            }
            catch (Exception e)
            {
                // The framework's message can carry absolute paths of the
                // agent's machine: it goes to the log, not to the caller.
                status = 500;
                kind = Kinds.OperationFailed;
                Log.Line("{0} {1}: {2}", req.HttpMethod, Path0(req.RawUrl), e);
                WriteEnvelope(ctx, 500, Failure(Kinds.OperationFailed, "internal error in the agent (" + e.GetType().Name + "); see its log", false, null));
            }
            finally
            {
                Log.Line("{0} {1} {2} {3} {4}ms", req.HttpMethod, Path0(req.RawUrl), status, kind, sw.ElapsedMilliseconds);
            }
        }

        // Path0 is the request path without its query, for messages.
        static string Path0(string rawUrl)
        {
            int q = rawUrl.IndexOf('?');
            return q >= 0 ? rawUrl.Substring(0, q) : rawUrl;
        }

        static Obj Failure(string kind, string message, bool fatal, object data)
        {
            var err = new Obj();
            err["kind"] = kind;
            err["message"] = message;
            err["fatal"] = fatal;
            var env = new Obj();
            env["ok"] = false;
            env["error"] = err;
            if (data != null) env["data"] = data;
            env["events"] = new object[0];
            return env;
        }

        static void WriteEnvelope(HttpListenerContext ctx, int status, Obj env)
        {
            Write(ctx, status, "application/json", Encoding.UTF8.GetBytes(Json.Write(env) + "\n"));
        }

        static void WriteFile(HttpListenerContext ctx, string path)
        {
            using (var f = new FileStream(path, FileMode.Open, FileAccess.Read, FileShare.Read))
            {
                try
                {
                    var resp = ctx.Response;
                    resp.StatusCode = 200;
                    resp.ContentType = "application/octet-stream";
                    resp.ContentLength64 = f.Length;
                    f.CopyTo(resp.OutputStream, 81920);
                    resp.OutputStream.Close();
                }
                catch (HttpListenerException e)
                {
                    Log.Line("reply lost: {0}", e.Message);
                }
            }
        }

        static void Write(HttpListenerContext ctx, int status, string contentType, byte[] body)
        {
            try
            {
                var resp = ctx.Response;
                resp.StatusCode = status;
                resp.ContentType = contentType;
                resp.ContentLength64 = body.Length;
                resp.OutputStream.Write(body, 0, body.Length);
                resp.OutputStream.Close();
            }
            catch (Exception e)
            {
                // The client went away; nothing to tell it.
                Log.Line("reply lost: {0}", e.Message);
            }
        }
    }
}
