// Pins.cs: trust on first use, then pin (siemens-ide.md section 3.8).
//
// A device pin is the identity of the CPU at one address, as tiad first
// saw it: its order number and firmware, read from the station an upload
// brought into the project. Every later online call to that address
// (upload, compare, download) refuses when the CPU's identity differs.
// The serial number and the MAC address are fields of the pin too, held
// as "not read" (null, never an empty string, which could match) until an
// online read of them is proven; a value is added only through an
// audited re-pin, never silently.
//
// What it catches: a different model or firmware at the address. What it
// does not: an identical CPU swapped in, or a machine in the middle on the
// first contact. The certificate pin after nautilus's first download is
// the plan for those (S2b).
//
// A second copy of tiad.DeviceIdentity's rules (siemens/tiad/pins.go),
// checked against the Go copy over the wire. Change both or neither.

using System;
using System.Collections.Generic;
using System.IO;
using System.Text;

namespace Tiad
{
    public sealed class DeviceIdentity
    {
        public const int MaxField = 128;

        public string Order = "", Firmware = "";
        // Serial and Mac are null when not read.
        public string Serial, Mac;

        public static DeviceIdentity Read(Reader r)
        {
            var d = new DeviceIdentity();
            if (r == null) return d;
            d.Order = r.Str("order");
            d.Firmware = r.Str("firmware");
            d.Serial = r.OptStr("serial");
            d.Mac = r.OptStr("mac");
            return d;
        }

        public Obj ToJson()
        {
            var o = new Obj();
            o["order"] = Order;
            o["firmware"] = Firmware;
            o["serial"] = Serial;
            o["mac"] = Mac;
            return o;
        }

        // Holds names the fields the identity holds, in order: order and
        // firmware always, serial and mac when read.
        public List<string> Holds()
        {
            var h = new List<string> { "order", "firmware" };
            if (Serial != null) h.Add("serial");
            if (Mac != null) h.Add("mac");
            return h;
        }

        // Validate is tiad.DeviceIdentity.Validate: why the identity is not
        // one tiad can pin, or null.
        public string Validate()
        {
            var errs = new List<string>();
            Check(errs, "order", Order, true);
            Check(errs, "firmware", Firmware, true);
            Check(errs, "serial", Serial, false);
            Check(errs, "mac", Mac, false);
            return errs.Count == 0 ? null : "device: " + string.Join("; ", errs.ToArray());
        }

        static void Check(List<string> errs, string name, string v, bool required)
        {
            if (v == null)
            {
                if (required) errs.Add("device." + name + " is empty");
                return;
            }
            if (v == "")
            {
                errs.Add(required ? "device." + name + " is empty" : "device." + name + " is empty (leave it out when it was not read)");
                return;
            }
            if (v.Length > MaxField) errs.Add("device." + name + " is longer than " + MaxField + " characters");
            foreach (char c in v)
            {
                if (c < 0x20 || c == 0x7f)
                {
                    errs.Add("device." + name + " has a control character");
                    break;
                }
            }
        }

        static string Show(string v)
        {
            return v == null ? "not read" : Json.Quote(v);
        }

        // Describe is the identity in words, for messages and the audit.
        public string Describe()
        {
            return "order " + Show(Order) + ", firmware " + Show(Firmware) + ", serial " + Show(Serial) + ", mac " + Show(Mac);
        }

        // Mismatch is tiad.DeviceMismatch: how now differs from the pin, or
        // null when it matches. Every field the pin holds must be read now
        // and equal; a field the pin does not hold is not compared.
        public static string Mismatch(DeviceIdentity pinned, DeviceIdentity now)
        {
            var diffs = new List<string>();
            Compare(diffs, "order", pinned.Order, now.Order);
            Compare(diffs, "firmware", pinned.Firmware, now.Firmware);
            Compare(diffs, "serial", pinned.Serial, now.Serial);
            Compare(diffs, "mac", pinned.Mac, now.Mac);
            return diffs.Count == 0 ? null : string.Join("; ", diffs.ToArray());
        }

        static void Compare(List<string> diffs, string name, string pinned, string now)
        {
            if (pinned == null) return;
            if (now == null) diffs.Add(name + " is pinned but was not read now");
            else if (!string.Equals(pinned, now, StringComparison.Ordinal)) diffs.Add(name + " " + Json.Quote(pinned) + " is now " + Json.Quote(now));
        }

        // Unpinned names the fields now holds that the pin does not: a
        // re-pin adds them, nothing else does.
        public static List<string> Unpinned(DeviceIdentity pinned, DeviceIdentity now)
        {
            var l = new List<string>();
            if (pinned.Serial == null && now.Serial != null) l.Add("serial");
            if (pinned.Mac == null && now.Mac != null) l.Add("mac");
            return l;
        }
    }

    // DeviceAttestation is the operator's reading of the CPU's identity off
    // the device itself (its label, its display), standing in for the
    // online read tiad cannot make yet (#789, #1325). It is accepted only
    // where that read is missing: tiad compares it with the device pin at
    // the pin's precision, a mismatch is device_changed as a read one is,
    // and it never adds or changes a pin. Every use is audited. A "tighten
    // later" loosening (#616): it goes once the online read exists.
    //
    // A second copy of tiad.DeviceAttestation's rules (siemens/tiad/pins.go).
    // Change both or neither.
    public sealed class DeviceAttestation
    {
        public string By = "", Order = "", Firmware = "";

        // Read is null when the request has no deviceAttestation.
        public static DeviceAttestation Read(Reader r)
        {
            if (r == null) return null;
            var a = new DeviceAttestation();
            a.By = r.Str("by");
            a.Order = r.Str("order");
            a.Firmware = r.Str("firmware");
            return a;
        }

        // Validate is tiad.DeviceAttestation.Validate: why the attestation
        // is malformed, or null.
        public string Validate()
        {
            var errs = new List<string>();
            if (By.Trim(' ') == "") errs.Add("deviceAttestation.by is empty");
            else if (Encoding.UTF8.GetByteCount(By) > Attestation.MaxBy) errs.Add("deviceAttestation.by is longer than " + Attestation.MaxBy + " bytes");
            else if (HasControl(By)) errs.Add("deviceAttestation.by has a control character");
            Field(errs, "order", Order);
            Field(errs, "firmware", Firmware);
            if (Firmware.Trim(' ') != "" && Firmware.Length <= DeviceIdentity.MaxField && !HasControl(Firmware) && Ascii(Firmware) && Version(Firmware) == null)
            {
                errs.Add("deviceAttestation.firmware " + Json.Quote(Firmware) + " is not a firmware version (V3.1.3, or 3.1.3)");
            }
            return errs.Count == 0 ? null : string.Join("; ", errs.ToArray());
        }

        static void Field(List<string> errs, string name, string v)
        {
            if (v.Trim(' ') == "") errs.Add("deviceAttestation." + name + " is empty");
            else if (v.Length > DeviceIdentity.MaxField) errs.Add("deviceAttestation." + name + " is longer than " + DeviceIdentity.MaxField + " characters");
            else if (HasControl(v)) errs.Add("deviceAttestation." + name + " has a control character");
            else if (!Ascii(v)) errs.Add("deviceAttestation." + name + " is not printable ASCII (as an order number and a firmware version are)");
        }

        // Ascii is whether v holds printable ASCII only.
        static bool Ascii(string v)
        {
            foreach (char c in v)
            {
                if (c < 0x20 || c > 0x7e) return false;
            }
            return true;
        }

        static bool HasControl(string v)
        {
            foreach (char c in v)
            {
                if (c < 0x20 || c == 0x7f) return true;
            }
            return false;
        }

        // Version is a firmware version's numeric parts ("V3.1.3" and
        // "3.1.3" are 3, 1, 3), leading zeros dropped; null when it is not
        // one.
        static List<string> Version(string v)
        {
            v = v.Trim(' ');
            if (v.StartsWith("V", StringComparison.Ordinal) || v.StartsWith("v", StringComparison.Ordinal)) v = v.Substring(1);
            var parts = new List<string>();
            foreach (string p in v.Split('.'))
            {
                if (p == "") return null;
                foreach (char c in p)
                {
                    if (c < '0' || c > '9') return null;
                }
                string n = p.TrimStart('0');
                parts.Add(n == "" ? "0" : n);
            }
            return parts;
        }

        // OrderKey is an order number as compared: spaces dropped, ASCII
        // letters upper case ("6ES7 510-1DK03-0AB0" is "6ES7510-1DK03-0AB0").
        // Nothing else is folded, so a character outside ASCII matches only
        // itself (the attestation holds none).
        static string OrderKey(string v)
        {
            var b = new StringBuilder();
            foreach (char c in v)
            {
                if (c == ' ') continue;
                b.Append(c >= 'a' && c <= 'z' ? (char)(c - 'a' + 'A') : c);
            }
            return b.ToString();
        }

        // Gap is tiad.DeviceAttestationGap: why the attestation cannot
        // stand in for the pin's check at all, or null. It states the order
        // number and firmware only, so a pin holding a serial or MAC fails
        // closed, and a firmware stated less precisely than the pin's
        // cannot match it.
        public static string Gap(DeviceIdentity pinned, DeviceAttestation a)
        {
            var gaps = new List<string>();
            if (pinned.Serial != null) gaps.Add("the pin holds a serial number, which an attestation does not state");
            if (pinned.Mac != null) gaps.Add("the pin holds a MAC address, which an attestation does not state");
            List<string> p = Version(pinned.Firmware), n = Version(a.Firmware);
            if (p == null)
            {
                gaps.Add("the pin's firmware " + Json.Quote(pinned.Firmware) + " is not a version an attestation can be compared with: re-pin it as one (POST /v1/pins/<address> with replace: true, audited)");
            }
            else if (n != null && n.Count < p.Count)
            {
                gaps.Add("the attested firmware " + Json.Quote(a.Firmware) + " is less precise than the pin's " + Json.Quote(pinned.Firmware) + ": read the full version off the CPU");
            }
            return gaps.Count == 0 ? null : string.Join("; ", gaps.ToArray());
        }

        // Mismatch is tiad.DeviceAttestationMismatch: how the attested
        // identity differs from the pin, at the pin's precision, or null.
        // The order number compares without whitespace or case; the
        // firmware on the pin's parts (a "V3.1" pin matches "3.1.3"). A pin
        // firmware that is not a version compares exactly.
        public static string Mismatch(DeviceIdentity pinned, DeviceAttestation a)
        {
            var diffs = new List<string>();
            if (OrderKey(pinned.Order) != OrderKey(a.Order)) diffs.Add("order " + Json.Quote(pinned.Order) + " is attested as " + Json.Quote(a.Order));
            List<string> p = Version(pinned.Firmware), n = Version(a.Firmware);
            bool same;
            if (p == null || n == null) same = false; // Gap and Validate refuse both first
            else
            {
                same = n.Count >= p.Count;
                for (int i = 0; same && i < p.Count; i++) same = p[i] == n[i];
            }
            if (!same) diffs.Add("firmware " + Json.Quote(pinned.Firmware) + " is attested as " + Json.Quote(a.Firmware));
            return diffs.Count == 0 ? null : string.Join("; ", diffs.ToArray());
        }

        // Identity is the attested identity, for messages: order and
        // firmware as stated, serial and MAC not read.
        public DeviceIdentity Identity()
        {
            return new DeviceIdentity { Order = Order, Firmware = Firmware };
        }

        // Audit is the attestation for an audit line, every part quoted so
        // none can forge a line.
        public string Audit()
        {
            return "{by=" + Json.Quote(By) + " order=" + Json.Quote(Order) + " firmware=" + Json.Quote(Firmware) + "}";
        }
    }

    // DeviceAttestationRecord is a device attestation tiad accepted: what
    // the operator stated, the pin it was compared with, and when (tiad's
    // clock), for the response and the deploy record.
    public sealed class DeviceAttestationRecord
    {
        public DeviceAttestation Attestation;
        public DeviceIdentity Pinned;
        public string At = "";

        public Obj ToJson()
        {
            var o = new Obj();
            o["by"] = Attestation.By;
            o["order"] = Attestation.Order;
            o["firmware"] = Attestation.Firmware;
            o["pinned"] = Pinned.ToJson();
            o["at"] = At;
            return o;
        }
    }

    // PinRead is what reading one pin file found.
    public enum PinState
    {
        None,         // no file: the address is not pinned
        Ok,           // a valid pin
        Corrupt,      // the file was read, but it is not a valid pin
        Inaccessible, // the file or its directory could not be read
    }

    public sealed class PinRead
    {
        public PinState State;
        public DeviceIdentity Device;
        public string Why = "";
        // Raw is the file's first line, for the audit of a corrupt pin.
        public string Raw = "";
    }

    // PinStore keeps one file per pinned CPU address in the pins directory
    // (TIAD_PINS_DIR): <address>.device, whose first line is the identity
    // as JSON, then comment lines saying what it is and how to re-pin. The
    // directory is outside the work dir, so the files API never reaches it.
    //
    // Only a file that is not there is "no pin". Any other failure to read
    // one, a missing or unreadable directory included, fails closed: a pin
    // that cannot be read must never let a changed CPU through.
    public sealed class PinStore
    {
        readonly string dir;
        readonly object mu = new object();

        PinStore(string dir)
        {
            this.dir = dir;
        }

        // Open creates the pins directory if needed and proves it usable
        // (a listing, and a file written, read back and removed). The agent
        // refuses to start otherwise.
        public static PinStore Open(string dir)
        {
            try
            {
                Directory.CreateDirectory(dir);
                Directory.GetFiles(dir);
                string probe = Path.Combine(dir, ".probe-" + Guid.NewGuid().ToString("N"));
                File.WriteAllText(probe, "tiad pins directory probe\n");
                if (File.ReadAllText(probe) != "tiad pins directory probe\n") throw new IOException("a probe file read back differently");
                File.Delete(probe);
            }
            catch (Exception e)
            {
                throw new ConfigException("TIAD_PINS_DIR " + dir + " is not usable (" + e.GetType().Name + ": " + e.Message +
                    "): tiad refuses to start rather than read every device pin as missing");
            }
            return new PinStore(dir);
        }

        public string Dir
        {
            get { return dir; }
        }

        string DeviceFile(string address)
        {
            // The address names a file: only a strict dotted quad may.
            if (!Ip.IsIPv4(address)) throw new ArgumentException("not an IPv4 address: " + address);
            return Path.Combine(dir, address + ".device");
        }

        public static TiadException Unreadable(string address, string why)
        {
            Log.Line("audit pin unreadable address={0} why={1}", Json.Quote(address), Json.Quote(why));
            return new TiadException(500, Kinds.OperationFailed,
                "the device pin for " + address + " is unreadable (" + why + "); tiad refuses online calls to " + address +
                " until it can be read, or, if its content is corrupt, until it is replaced: POST /v1/pins/" + address + " with replace: true (audited)");
        }

        // Read reads the pin file for address and says what it found.
        public PinRead Read(string address)
        {
            lock (mu)
            {
                string f = DeviceFile(address);
                string first;
                try
                {
                    using (var fs = new FileStream(f, FileMode.Open, FileAccess.Read, FileShare.Read))
                    using (var r = new StreamReader(fs, new UTF8Encoding(false)))
                    {
                        first = r.ReadLine() ?? "";
                    }
                }
                catch (FileNotFoundException)
                {
                    return new PinRead { State = PinState.None };
                }
                catch (Exception e)
                {
                    return new PinRead { State = PinState.Inaccessible, Why = e.GetType().Name + ": " + e.Message };
                }
                DeviceIdentity d;
                try
                {
                    var root = Reader.Root(Json.Parse(first));
                    d = DeviceIdentity.Read(root);
                    root.Done();
                }
                catch (TiadException e)
                {
                    return new PinRead { State = PinState.Corrupt, Why = e.Message, Raw = first };
                }
                string bad = d.Validate();
                if (bad != null) return new PinRead { State = PinState.Corrupt, Why = bad, Raw = first };
                return new PinRead { State = PinState.Ok, Device = d };
            }
        }

        // Device is the device pin for address, or null when there is none.
        // A pin tiad cannot read fails closed.
        public DeviceIdentity Device(string address)
        {
            PinRead r = Read(address);
            if (r.State == PinState.None) return null;
            if (r.State == PinState.Ok) return r.Device;
            throw Unreadable(address, r.Why);
        }

        // SetDevice writes the device pin for address, replacing any, in
        // one step: a new file renamed over the old.
        public void SetDevice(string address, DeviceIdentity d, string how)
        {
            lock (mu)
            {
                string f = DeviceFile(address);
                var b = new StringBuilder();
                b.Append(Json.Write(d.ToJson())).Append('\n');
                b.Append("# tiad device pin for the CPU at ").Append(address).Append(": ").Append(how).Append(", ")
                    .Append(DateTime.UtcNow.ToString("yyyy-MM-ddTHH:mm:ssZ")).Append('\n');
                b.Append("# holds: ").Append(string.Join(", ", d.Holds().ToArray())).Append("; null is not read\n");
                b.Append("# re-pin with POST /v1/pins/").Append(address).Append(" {\"device\": ..., \"replace\": true, \"announced\": true} (audited); never edit by hand\n");
                string tmp = f + ".new";
                File.WriteAllText(tmp, b.ToString(), new UTF8Encoding(false));
                if (File.Exists(f)) File.Replace(tmp, f, null);
                else File.Move(tmp, f);
            }
        }

        // Addresses are the pinned addresses, sorted. A directory that
        // cannot be listed fails closed.
        public List<string> Addresses()
        {
            lock (mu)
            {
                string[] files;
                try
                {
                    files = Directory.GetFiles(dir, "*.device");
                }
                catch (Exception e)
                {
                    Log.Line("audit pin unreadable dir={0} why={1}", Json.Quote(dir), Json.Quote(e.GetType().Name + ": " + e.Message));
                    throw new TiadException(500, Kinds.OperationFailed,
                        "the pins directory cannot be listed (" + e.GetType().Name + ": " + e.Message + "); tiad refuses online calls to pinned CPUs until it can be read");
                }
                var l = new List<string>();
                foreach (string f in files)
                {
                    // Windows' pattern also matches a longer extension.
                    if (!string.Equals(Path.GetExtension(f), ".device", StringComparison.Ordinal)) continue;
                    string a = Path.GetFileNameWithoutExtension(f);
                    if (Ip.IsIPv4(a)) l.Add(a);
                }
                l.Sort(StringComparer.Ordinal);
                return l;
            }
        }
    }
}
