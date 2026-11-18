// Server.cs: HTTP, the token and the envelope (siemens-ide.md §3.2).
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

    // Bytes is a reply that is raw bytes, not an envelope.
    public sealed class Bytes
    {
        public readonly byte[] Data;

        public Bytes(byte[] data)
        {
            Data = data;
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

        // Json reads the body as one JSON object, strictly (see Reader).
        public Reader Json()
        {
            string text;
            using (var sr = new StreamReader(Request.InputStream, new UTF8Encoding(false)))
            {
                text = sr.ReadToEnd();
            }
            if (text.Trim() == "") throw TiadException.BadRequest("body: EOF");
            return Reader.Root(Tiad.Json.Parse(text));
        }
    }

    public sealed class Router
    {
        sealed class Route
        {
            public string Method;
            public string[] Pattern;
            public Handler Handler;
        }

        readonly List<Route> routes = new List<Route>();

        // Add registers a handler. A pattern is "/v1/a/{name}/b" (one
        // segment) or "/v1/files/{path...}" (the rest, possibly empty).
        public void Add(string method, string pattern, Handler h)
        {
            routes.Add(new Route { Method = method, Pattern = pattern.TrimStart('/').Split('/'), Handler = h });
        }

        internal Handler Match(string method, List<string> segs, out Dictionary<string, string> vars, out bool pathKnown)
        {
            pathKnown = false;
            foreach (var r in routes)
            {
                var v = new Dictionary<string, string>(StringComparer.Ordinal);
                if (!Fits(r.Pattern, segs, v)) continue;
                pathKnown = true;
                if (r.Method != method) continue;
                vars = v;
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
            want = cfg.Token == "" ? null : Encoding.UTF8.GetBytes("Bearer " + cfg.Token);
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
                if (host == "0.0.0.0" || host == "*") host = "+";
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
                Url = "http://" + (host == "+" ? "0.0.0.0" : cfg.Host) + ":" + port;
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
            if (want == null) return true;
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
                var raw = data as Bytes;
                if (raw != null)
                {
                    Write(ctx, 200, "application/octet-stream", raw.Data);
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
                status = 500;
                kind = Kinds.OperationFailed;
                WriteEnvelope(ctx, 500, Failure(Kinds.OperationFailed, e.GetType().Name + ": " + e.Message, false, null));
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
