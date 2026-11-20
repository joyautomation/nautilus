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
