// Paths.cs: the work-dir sandbox (siemens-ide.md §3.4).
//
// Every path in a request is relative to the work dir and obeys one rule,
// tiad.CleanPath in the Go client. The client checks each path and the
// agent checks again here, so this file is a second copy of that rule. The
// contract suite (siemens/tiad/tiadtest) checks the two against one table
// over the wire. Change both or neither.

using System;
using System.Collections.Generic;
using System.Globalization;
using System.IO;
using System.Text;

namespace Tiad
{
    public static class Paths
    {
        // GoClean is Go's path.Clean, ported line for line: the shortest
        // path equivalent to p by purely lexical processing.
        public static string GoClean(string p)
        {
            if (p == "") return ".";
            bool rooted = p[0] == '/';
            int n = p.Length;
            var outb = new StringBuilder(n);
            int r = 0, dotdot = 0;
            if (rooted)
            {
                outb.Append('/');
                r = 1;
                dotdot = 1;
            }
            while (r < n)
            {
                if (p[r] == '/')
                {
                    r++;
                }
                else if (p[r] == '.' && (r + 1 == n || p[r + 1] == '/'))
                {
                    r++;
                }
                else if (p[r] == '.' && p[r + 1] == '.' && (r + 2 == n || p[r + 2] == '/'))
                {
                    r += 2;
                    if (outb.Length > dotdot)
                    {
                        outb.Length--;
                        while (outb.Length > dotdot && outb[outb.Length - 1] != '/')
                        {
                            outb.Length--;
                        }
                        // Go backs up to just before the slash; drop it too.
                        if (outb.Length > dotdot && outb[outb.Length - 1] == '/')
                        {
                            outb.Length--;
                        }
                    }
                    else if (!rooted)
                    {
                        if (outb.Length > 0) outb.Append('/');
                        outb.Append("..");
                        dotdot = outb.Length;
                    }
                }
                else
                {
                    if (rooted && outb.Length != 1 || !rooted && outb.Length != 0)
                    {
                        outb.Append('/');
                    }
                    for (; r < n && p[r] != '/'; r++)
                    {
                        outb.Append(p[r]);
                    }
                }
            }
            if (outb.Length == 0) return ".";
            return outb.ToString();
        }

        static readonly HashSet<string> Reserved = MakeReserved();

        static HashSet<string> MakeReserved()
        {
            // CON, PRN, AUX, NUL, and COM and LPT followed by a digit 0-9
            // or a superscript one, two or three: Windows reserves them in
            // every directory.
            var m = new HashSet<string>(StringComparer.Ordinal) { "CON", "PRN", "AUX", "NUL" };
            foreach (string d in new[] { "0", "1", "2", "3", "4", "5", "6", "7", "8", "9", "\u00b9", "\u00b2", "\u00b3" })
            {
                m.Add("COM" + d);
                m.Add("LPT" + d);
            }
            return m;
        }

        // CleanPath applies the sandbox rule and returns the cleaned path:
        // relative, forward slashes, no "..", no drive letter or other ':',
        // no control character or <>"|?*, no segment ending in a dot or a
        // space, no Windows device name (with or without an extension).
        public static string CleanPath(string p)
        {
            if (p == "") throw Bad(p, "empty");
            if (p.StartsWith("/", StringComparison.Ordinal) || p.IndexOf('\\') >= 0 || p.IndexOf(':') >= 0)
            {
                throw Bad(p, "absolute, a backslash or a ':'");
            }
            foreach (char c in p)
            {
                if (c < 0x20 || c == 0x7f || "<>\"|?*".IndexOf(c) >= 0)
                {
                    throw Bad(p, "a control character or one of <>\"|?*");
                }
            }
            string cl = GoClean(p);
            if (cl == "." || cl == ".." || cl.StartsWith("../", StringComparison.Ordinal))
            {
                throw Bad(p, "outside the work dir");
            }
            foreach (string seg in cl.Split('/'))
            {
                if (seg.EndsWith(".", StringComparison.Ordinal) || seg.EndsWith(" ", StringComparison.Ordinal))
                {
                    throw Bad(p, "a name ending in a dot or a space");
                }
                string stem = seg.ToUpperInvariant();
                int dot = stem.IndexOf('.');
                if (dot >= 0) stem = stem.Substring(0, dot);
                stem = stem.TrimEnd(' ');
                if (Reserved.Contains(stem)) throw Bad(p, "a Windows device name");
            }
            return cl;
        }

        static TiadException Bad(string p, string why)
        {
            return TiadException.BadRequest("path {0} is not allowed in the work dir: {1}", Json.Quote(p), why);
        }
    }

    // WorkDir is the agent's scratch directory. Every file a request names
    // is inside it.
    public sealed class WorkDir
    {
        public readonly string Root;
        static readonly StringComparison Cmp = Path.DirectorySeparatorChar == '\\'
            ? StringComparison.OrdinalIgnoreCase : StringComparison.Ordinal;

        public WorkDir(string root)
        {
            Root = Path.GetFullPath(root).TrimEnd(Path.DirectorySeparatorChar);
        }

        // Resolve turns a request path into a full path inside the work
        // dir. The lexical rule comes first (CleanPath). Then, because the
        // file system is not lexical: the full path must still be inside
        // the work dir, and no part of it that exists may be a link
        // (symbolic link, junction), which could point anywhere.
        public string Resolve(string requestPath)
        {
            string clean = Paths.CleanPath(requestPath);
            string full;
            try
            {
                full = Path.GetFullPath(Path.Combine(Root, clean.Replace('/', Path.DirectorySeparatorChar)));
            }
            catch (Exception e)
            {
                if (e is PathTooLongException || e is NotSupportedException || e is ArgumentException)
                {
                    throw TiadException.BadRequest("path {0} is not usable here: {1}", Json.Quote(requestPath), e.GetType().Name);
                }
                throw;
            }
            if (!full.StartsWith(Root + Path.DirectorySeparatorChar, Cmp))
            {
                throw TiadException.BadRequest("path {0} resolves outside the work dir", Json.Quote(requestPath));
            }
            string cur = Root;
            foreach (string seg in clean.Split('/'))
            {
                cur = Path.Combine(cur, seg);
                FileAttributes a;
                try
                {
                    a = File.GetAttributes(cur);
                }
                catch (FileNotFoundException)
                {
                    break;
                }
                catch (DirectoryNotFoundException)
                {
                    break;
                }
                catch (UnauthorizedAccessException)
                {
                    throw new TiadException(403, Kinds.BadRequest, "path " + Json.Quote(requestPath) + ": access denied");
                }
                catch (IOException e)
                {
                    // A name too long for the file system, or a part of
                    // the path that is a file.
                    throw TiadException.BadRequest("path {0} is not usable here: {1}", Json.Quote(requestPath), e.GetType().Name);
                }
                if ((a & FileAttributes.ReparsePoint) != 0)
                {
                    throw TiadException.BadRequest("path {0} goes through a link, which the work dir does not allow", Json.Quote(requestPath));
                }
            }
            return full;
        }

        // Rel is the request form of a full path inside the work dir.
        public string Rel(string full)
        {
            return full.Substring(Root.Length + 1).Replace(Path.DirectorySeparatorChar, '/');
        }
    }
}
