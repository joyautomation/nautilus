// Duration.cs: Go's time.ParseDuration and Duration.String, ported, so a
// station's update times ("16ms", "0.016s", "16000us") mean here what
// they mean to siemens/station, and its messages read the same.

using System;
using System.Collections.Generic;
using System.Text;

namespace Tiad
{
    public static class GoDuration
    {
        const ulong Max = 1UL << 63;

        static readonly Dictionary<string, ulong> Units = new Dictionary<string, ulong>(StringComparer.Ordinal)
        {
            { "ns", 1UL },
            { "us", 1000UL },
            { "\u00b5s", 1000UL }, // micro sign
            { "\u03bcs", 1000UL }, // Greek small letter mu
            { "ms", 1000000UL },
            { "s", 1000000000UL },
            { "m", 60000000000UL },
            { "h", 3600000000000UL },
        };

        // TryParse reads a duration in nanoseconds: [-+]?([0-9]*(\.[0-9]*)?[a-z]+)+
        public static bool TryParse(string orig, out long d)
        {
            d = 0;
            string s = orig;
            bool neg = false;
            if (s != "" && (s[0] == '-' || s[0] == '+'))
            {
                neg = s[0] == '-';
                s = s.Substring(1);
            }
            if (s == "0") return true;
            if (s == "") return false;
            ulong total = 0;
            while (s != "")
            {
                ulong v = 0, f = 0;
                double scale = 1;
                if (!(s[0] == '.' || s[0] >= '0' && s[0] <= '9')) return false;
                int pl = s.Length;
                int i = 0;
                for (; i < s.Length && s[i] >= '0' && s[i] <= '9'; i++)
                {
                    if (v > Max / 10) return false;
                    v = v * 10 + (ulong)(s[i] - '0');
                    if (v > Max) return false;
                }
                s = s.Substring(i);
                bool pre = pl != s.Length;
                bool post = false;
                if (s != "" && s[0] == '.')
                {
                    s = s.Substring(1);
                    pl = s.Length;
                    bool overflow = false;
                    i = 0;
                    for (; i < s.Length && s[i] >= '0' && s[i] <= '9'; i++)
                    {
                        if (overflow) continue;
                        if (f > (Max - 1) / 10)
                        {
                            overflow = true;
                            continue;
                        }
                        ulong y = f * 10 + (ulong)(s[i] - '0');
                        if (y > Max)
                        {
                            overflow = true;
                            continue;
                        }
                        f = y;
                        scale *= 10;
                    }
                    s = s.Substring(i);
                    post = pl != s.Length;
                }
                if (!pre && !post) return false;
                i = 0;
                for (; i < s.Length; i++)
                {
                    char c = s[i];
                    if (c == '.' || c >= '0' && c <= '9') break;
                }
                if (i == 0) return false;
                string u = s.Substring(0, i);
                s = s.Substring(i);
                ulong unit;
                if (!Units.TryGetValue(u, out unit)) return false;
                if (v > Max / unit) return false;
                v *= unit;
                if (f > 0)
                {
                    v += (ulong)((double)f * ((double)unit / scale));
                    if (v > Max) return false;
                }
                total += v;
                if (total > Max) return false;
            }
            if (neg)
            {
                d = total == Max ? long.MinValue : -(long)total;
                return true;
            }
            if (total > Max - 1) return false;
            d = (long)total;
            return true;
        }

        // Format is Go's Duration.String: "16ms", "1.5s", "1h2m0s", "0s".
        public static string Format(long d)
        {
            var sb = new StringBuilder();
            bool neg = d < 0;
            ulong u = neg ? (ulong)(-(d + 1)) + 1 : (ulong)d;
            if (u < 1000000000UL)
            {
                int prec;
                string suffix;
                if (u == 0) return "0s";
                if (u < 1000UL)
                {
                    prec = 0;
                    suffix = "ns";
                }
                else if (u < 1000000UL)
                {
                    prec = 3;
                    suffix = "\u00b5s";
                }
                else
                {
                    prec = 6;
                    suffix = "ms";
                }
                sb.Insert(0, suffix);
                u = Frac(sb, u, prec);
                sb.Insert(0, u.ToString(System.Globalization.CultureInfo.InvariantCulture));
            }
            else
            {
                sb.Insert(0, "s");
                u = Frac(sb, u, 9);
                sb.Insert(0, (u % 60).ToString(System.Globalization.CultureInfo.InvariantCulture));
                u /= 60;
                if (u > 0)
                {
                    sb.Insert(0, "m");
                    sb.Insert(0, (u % 60).ToString(System.Globalization.CultureInfo.InvariantCulture));
                    u /= 60;
                    if (u > 0)
                    {
                        sb.Insert(0, "h");
                        sb.Insert(0, u.ToString(System.Globalization.CultureInfo.InvariantCulture));
                    }
                }
            }
            if (neg) sb.Insert(0, "-");
            return sb.ToString();
        }

        // Frac prepends the fraction of v with prec digits, trailing zeros
        // and a bare point omitted, and returns v without it.
        static ulong Frac(StringBuilder sb, ulong v, int prec)
        {
            bool print = false;
            var digits = new StringBuilder();
            for (int i = 0; i < prec; i++)
            {
                ulong digit = v % 10;
                print = print || digit != 0;
                if (print) digits.Insert(0, (char)('0' + (int)digit));
                v /= 10;
            }
            if (print) sb.Insert(0, "." + digits);
            return v;
        }
    }
}
