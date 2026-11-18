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

        // Parse reads a JSON document. Malformed JSON is a bad request, and
        // so is anything JavaScriptSerializer would take that is not JSON
        // (single quotes, bare keys, comments, trailing commas, NaN, ...):
        // Go refuses those, so the agent must.
        public static object Parse(string text)
        {
            string bad = Syntax.Check(text);
            if (bad != null) throw TiadException.BadRequest("body: {0}", bad);
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

        // Quote is Go's %q (strconv.Quote), so messages read as Go's do:
        // printable characters as they are, \a \b \f \n \r \t \v, then
        // \xHH below space and for DEL, \uHHHH and \UHHHHHHHH for anything
        // else that is not printable, all in lower-case hex.
        public static string Quote(string s)
        {
            var b = new StringBuilder("\"");
            for (int i = 0; i < s.Length; i++)
            {
                int r = s[i];
                if (char.IsHighSurrogate(s, i) && i + 1 < s.Length && char.IsLowSurrogate(s, i + 1))
                {
                    r = char.ConvertToUtf32(s[i], s[i + 1]);
                }
                else if (char.IsSurrogate(s, i))
                {
                    r = 0xFFFD; // as Go reads an unpaired surrogate
                }
                if (r == '"' || r == '\\')
                {
                    b.Append('\\').Append((char)r);
                }
                else if (IsPrint(s, i, r))
                {
                    if (r > 0xFFFF) b.Append(s, i, 2);
                    else b.Append((char)r);
                }
                else
                {
                    switch (r)
                    {
                        case 0x07: b.Append("\\a"); break;
                        case 0x08: b.Append("\\b"); break;
                        case 0x0C: b.Append("\\f"); break;
                        case 0x0A: b.Append("\\n"); break;
                        case 0x0D: b.Append("\\r"); break;
                        case 0x09: b.Append("\\t"); break;
                        case 0x0B: b.Append("\\v"); break;
                        default:
                            if (r < 0x20 || r == 0x7F) b.Append("\\x").Append(r.ToString("x2", CultureInfo.InvariantCulture));
                            else if (r <= 0xFFFF) b.Append("\\u").Append(r.ToString("x4", CultureInfo.InvariantCulture));
                            else b.Append("\\U").Append(r.ToString("x8", CultureInfo.InvariantCulture));
                            break;
                    }
                }
                if (r > 0xFFFF) i++;
            }
            return b.Append('"').ToString();
        }

        // IsPrint is Go's unicode.IsPrint: letters, marks, numbers,
        // punctuation, symbols, and the ASCII space. The categories come
        // from the framework's Unicode tables, which are older than Go's:
        // a character assigned since then is escaped here and printed by
        // Go. Only a message's quoting can differ, never a rule.
        static bool IsPrint(string s, int i, int r)
        {
            if (r == ' ') return true;
            if (r == 0xFFFD) return true;
            switch (CharUnicodeInfo.GetUnicodeCategory(s, i))
            {
                case UnicodeCategory.UppercaseLetter:
                case UnicodeCategory.LowercaseLetter:
                case UnicodeCategory.TitlecaseLetter:
                case UnicodeCategory.ModifierLetter:
                case UnicodeCategory.OtherLetter:
                case UnicodeCategory.NonSpacingMark:
                case UnicodeCategory.SpacingCombiningMark:
                case UnicodeCategory.EnclosingMark:
                case UnicodeCategory.DecimalDigitNumber:
                case UnicodeCategory.LetterNumber:
                case UnicodeCategory.OtherNumber:
                case UnicodeCategory.ConnectorPunctuation:
                case UnicodeCategory.DashPunctuation:
                case UnicodeCategory.OpenPunctuation:
                case UnicodeCategory.ClosePunctuation:
                case UnicodeCategory.InitialQuotePunctuation:
                case UnicodeCategory.FinalQuotePunctuation:
                case UnicodeCategory.OtherPunctuation:
                case UnicodeCategory.MathSymbol:
                case UnicodeCategory.CurrencySymbol:
                case UnicodeCategory.ModifierSymbol:
                case UnicodeCategory.OtherSymbol:
                    return true;
                default:
                    return false;
            }
        }
    }

    // Syntax checks a document against RFC 8259 and nothing more lenient.
    static class Syntax
    {
        public static string Check(string s)
        {
            int i = 0;
            string err = Value(s, ref i, 0);
            if (err != null) return err;
            Ws(s, ref i);
            return i == s.Length ? null : "invalid character after the top-level value at offset " + i;
        }

        static void Ws(string s, ref int i)
        {
            while (i < s.Length && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r')) i++;
        }

        static string At(int i, string what)
        {
            return what + " at offset " + i.ToString(CultureInfo.InvariantCulture);
        }

        static string Value(string s, ref int i, int depth)
        {
            if (depth > 64) return At(i, "nesting too deep");
            Ws(s, ref i);
            if (i >= s.Length) return "unexpected end of JSON input";
            char c = s[i];
            if (c == '{')
            {
                i++;
                Ws(s, ref i);
                if (i < s.Length && s[i] == '}')
                {
                    i++;
                    return null;
                }
                // A key twice is refused: parsers disagree on which one
                // wins, and "stopCpu": false, "stopCpu": true must never
                // mean yes. Keys compare decoded, escapes and all.
                var keys = new HashSet<string>(StringComparer.Ordinal);
                while (true)
                {
                    Ws(s, ref i);
                    if (i >= s.Length || s[i] != '"') return At(i, "want a string key");
                    int keyAt = i;
                    var key = new StringBuilder();
                    string e = Str(s, ref i, key);
                    if (e != null) return e;
                    if (!keys.Add(key.ToString())) return At(keyAt, "duplicate key " + Json.Quote(key.ToString()));
                    Ws(s, ref i);
                    if (i >= s.Length || s[i] != ':') return At(i, "want ':'");
                    i++;
                    e = Value(s, ref i, depth + 1);
                    if (e != null) return e;
                    Ws(s, ref i);
                    if (i < s.Length && s[i] == ',')
                    {
                        i++;
                        continue;
                    }
                    if (i < s.Length && s[i] == '}')
                    {
                        i++;
                        return null;
                    }
                    return At(i, "want ',' or '}'");
                }
            }
            if (c == '[')
            {
                i++;
                Ws(s, ref i);
                if (i < s.Length && s[i] == ']')
                {
                    i++;
                    return null;
                }
                while (true)
                {
                    string e = Value(s, ref i, depth + 1);
                    if (e != null) return e;
                    Ws(s, ref i);
                    if (i < s.Length && s[i] == ',')
                    {
                        i++;
                        continue;
                    }
                    if (i < s.Length && s[i] == ']')
                    {
                        i++;
                        return null;
                    }
                    return At(i, "want ',' or ']'");
                }
            }
            if (c == '"') return Str(s, ref i, null);
            if (c == '-' || c >= '0' && c <= '9') return Num(s, ref i);
            foreach (string lit in new[] { "true", "false", "null" })
            {
                if (string.CompareOrdinal(s, i, lit, 0, lit.Length) == 0)
                {
                    i += lit.Length;
                    return null;
                }
            }
            return At(i, "invalid character");
        }

        // Str checks a string and, when value is not null, decodes it there.
        static string Str(string s, ref int i, StringBuilder value)
        {
            i++; // the opening quote
            while (i < s.Length)
            {
                char c = s[i];
                if (c == '"')
                {
                    i++;
                    return null;
                }
                if (c < 0x20) return At(i, "a control character in a string");
                if (c == '\\')
                {
                    i++;
                    if (i >= s.Length) break;
                    char e = s[i];
                    if (e == 'u')
                    {
                        for (int k = 1; k <= 4; k++)
                        {
                            if (i + k >= s.Length || Uri.IsHexDigit(s[i + k]) == false) return At(i, "a bad \\u escape");
                        }
                        if (value != null) value.Append((char)Convert.ToInt32(s.Substring(i + 1, 4), 16));
                        i += 5;
                        continue;
                    }
                    int x = "\"\\/bfnrt".IndexOf(e);
                    if (x < 0) return At(i, "a bad escape");
                    if (value != null) value.Append("\"\\/\b\f\n\r\t"[x]);
                    i++;
                    continue;
                }
                if (value != null) value.Append(c);
                i++;
            }
            return "unexpected end of JSON input in a string";
        }

        static bool Digit(string s, int i)
        {
            return i < s.Length && s[i] >= '0' && s[i] <= '9';
        }

        static string Num(string s, ref int i)
        {
            int start = i;
            if (s[i] == '-') i++;
            if (!Digit(s, i)) return At(start, "a bad number");
            if (s[i] == '0') i++;
            else while (Digit(s, i)) i++;
            if (i < s.Length && s[i] == '.')
            {
                i++;
                if (!Digit(s, i)) return At(start, "a bad number");
                while (Digit(s, i)) i++;
            }
            if (i < s.Length && (s[i] == 'e' || s[i] == 'E'))
            {
                i++;
                if (i < s.Length && (s[i] == '+' || s[i] == '-')) i++;
                if (!Digit(s, i)) return At(start, "a bad number");
                while (Digit(s, i)) i++;
            }
            return null;
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

        // Int returns an integer field, 0 when absent or null. A fraction
        // or an exponent is refused, as Go refuses it. So is a value
        // outside a 32-bit int, which Go's 64-bit int would take: no field
        // tiad reads (slots, I/O starts, milliseconds) needs more.
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
