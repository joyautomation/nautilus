// Json.cs: JSON in and out, on the framework's own serializer (no NuGet on
// the TIA machine).
//
// JavaScriptSerializer is lenient: it ignores unknown fields and converts
// types. The Go side decodes strictly (DisallowUnknownFields, typed
// fields), and the agent must refuse what the Go fake refuses, so request
// bodies are read through Reader, which checks every field's type and
// refuses any field nobody asked for. As in Go, a null field is the same
// as an absent one.

using System;
using System.Collections;
using System.Collections.Generic;
using System.Globalization;
using System.Text;
using System.Web.Script.Serialization;

namespace Tiad
{
    // Obj is a JSON object as tiad writes it, fields in insertion order.
    public sealed class Obj : Dictionary<string, object>
    {
    }

    public static class Json
    {
        static JavaScriptSerializer New()
        {
            var s = new JavaScriptSerializer();
            s.MaxJsonLength = Call.MaxJsonBody;
            s.RecursionLimit = 64;
            return s;
        }

        public static string Write(object v)
        {
            return New().Serialize(v);
        }

        // Parse reads a JSON document. Malformed JSON is a bad request.
        public static object Parse(string text)
        {
            try
            {
                return New().DeserializeObject(text);
            }
            catch (ArgumentException e)
            {
                throw TiadException.BadRequest("body: {0}", e.Message);
            }
            catch (InvalidOperationException e)
            {
                throw TiadException.BadRequest("body: {0}", e.Message);
            }
        }

        // Quote is Go's %q, near enough for messages: the string in double
        // quotes with quotes, backslashes and control characters escaped.
        public static string Quote(string s)
        {
            var b = new StringBuilder("\"");
            foreach (char c in s)
            {
                if (c == '"' || c == '\\')
                {
                    b.Append('\\').Append(c);
                }
                else if (c < 0x20 || c == 0x7f)
                {
                    b.Append("\\x").Append(((int)c).ToString("x2", CultureInfo.InvariantCulture));
                }
                else
                {
                    b.Append(c);
                }
            }
            return b.Append('"').ToString();
        }
    }

    // Reader reads one JSON object strictly. Ask for each field with the
    // typed getter, then call Done on the root, which refuses any field not
    // asked for, at any depth.
    public sealed class Reader
    {
        readonly string at;
        readonly IDictionary<string, object> o;
        readonly HashSet<string> asked = new HashSet<string>(StringComparer.Ordinal);
        // tree holds every reader made from one root, the root included.
        readonly List<Reader> tree;

        Reader(string at, IDictionary<string, object> o, List<Reader> tree)
        {
            this.at = at;
            this.o = o;
            this.tree = tree;
            tree.Add(this);
        }

        // Root reads a parsed body, which must be an object.
        public static Reader Root(object v)
        {
            var d = v as IDictionary<string, object>;
            if (d == null)
            {
                throw TiadException.BadRequest("body: want a JSON object");
            }
            return new Reader("", d, new List<Reader>());
        }

        string Name(string key)
        {
            return at == "" ? key : at + "." + key;
        }

        object Get(string key)
        {
            asked.Add(key);
            object v;
            return o.TryGetValue(key, out v) ? v : null;
        }

        TiadException Wrong(string key, string want, object got)
        {
            return TiadException.BadRequest("body: {0}: want {1}, got {2}", Name(key), want, TypeName(got));
        }

        static string TypeName(object v)
        {
            if (v == null) return "null";
            if (v is string) return "a string";
            if (v is bool) return "a boolean";
            if (v is int || v is long || v is decimal || v is double) return "a number";
            if (v is IDictionary<string, object>) return "an object";
            if (v is IList) return "an array";
            return v.GetType().Name;
        }

        // Str returns a string field, "" when absent or null.
        public string Str(string key)
        {
            var v = Get(key);
            if (v == null) return "";
            var s = v as string;
            if (s == null) throw Wrong(key, "a string", v);
            return s;
        }

        // Bool returns a boolean field, false when absent or null.
        public bool Bool(string key)
        {
            var v = OptBool(key);
            return v.HasValue && v.Value;
        }

        // OptBool returns a boolean field, null when absent or null: "not
        // answered", which is different from false.
        public bool? OptBool(string key)
        {
            var v = Get(key);
            if (v == null) return null;
            if (!(v is bool)) throw Wrong(key, "a boolean", v);
            return (bool)v;
        }

        // Int returns an integer field, 0 when absent or null. A fraction,
        // an exponent or a value outside int is refused, as Go refuses it.
        public int Int(string key)
        {
            var v = OptInt(key);
            return v.HasValue ? v.Value : 0;
        }

        public int? OptInt(string key)
        {
            var v = Get(key);
            if (v == null) return null;
            if (v is int) return (int)v;
            if (v is long)
            {
                long l = (long)v;
                if (l < int.MinValue || l > int.MaxValue) throw Wrong(key, "an int", v);
                return (int)l;
            }
            throw Wrong(key, "an integer", v);
        }

        // Strs returns a list of strings, empty when absent or null.
        public List<string> Strs(string key)
        {
            var v = Get(key);
            var outp = new List<string>();
            if (v == null) return outp;
            var l = v as IList;
            if (l == null || v is string) throw Wrong(key, "an array of strings", v);
            foreach (var x in l)
            {
                var s = x as string;
                if (s == null) throw Wrong(key, "an array of strings", x);
                outp.Add(s);
            }
            return outp;
        }

        // StrMap returns an object of strings, empty when absent or null.
        public Dictionary<string, string> StrMap(string key)
        {
            var v = Get(key);
            var outp = new Dictionary<string, string>(StringComparer.Ordinal);
            if (v == null) return outp;
            var d = v as IDictionary<string, object>;
            if (d == null) throw Wrong(key, "an object of strings", v);
            foreach (var kv in d)
            {
                var s = kv.Value as string;
                if (s == null && kv.Value != null) throw Wrong(key + "." + kv.Key, "a string", kv.Value);
                outp[kv.Key] = s ?? "";
            }
            return outp;
        }

        // Child returns an object field, null when absent or null.
        public Reader Child(string key)
        {
            var v = Get(key);
            if (v == null) return null;
            var d = v as IDictionary<string, object>;
            if (d == null) throw Wrong(key, "an object", v);
            return new Reader(Name(key), d, tree);
        }

        // Children returns an array of objects, empty when absent or null.
        public List<Reader> Children(string key)
        {
            var v = Get(key);
            var outp = new List<Reader>();
            if (v == null) return outp;
            var l = v as IList;
            if (l == null || v is string) throw Wrong(key, "an array of objects", v);
            for (int i = 0; i < l.Count; i++)
            {
                var d = l[i] as IDictionary<string, object>;
                if (d == null) throw Wrong(key, "an array of objects", l[i]);
                outp.Add(new Reader(Name(key) + "[" + i.ToString(CultureInfo.InvariantCulture) + "]", d, tree));
            }
            return outp;
        }

        // Done refuses any field that no getter asked for, in this object
        // and in every object read from the same root, so a misspelt field
        // is an error, not a silently ignored request.
        public void Done()
        {
            foreach (var r in tree)
            {
                foreach (var k in r.o.Keys)
                {
                    if (!r.asked.Contains(k))
                    {
                        throw TiadException.BadRequest("body: json: unknown field {0}", Json.Quote(r.Name(k)));
                    }
                }
            }
        }
    }
}
