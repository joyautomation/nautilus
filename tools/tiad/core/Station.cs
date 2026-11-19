// Station.cs: the station spec (siemens-ide.md section 3.3) and its rules.
//
// A second copy of tiad.Station.Validate and the siemens/station
// primitives. The messages are Go's, word for word, and the contract suite
// checks both copies with one table over the wire (tiadtest.StationCases).
// Change both or neither.

using System;
using System.Collections.Generic;
using System.Globalization;
using System.Text;

namespace Tiad
{
    public sealed class Cpu
    {
        public string Name = "", Order = "", Firmware = "", Address = "", SubnetMask = "", Router = "", ProfinetName = "";

        public static Cpu Read(Reader r)
        {
            var c = new Cpu();
            if (r == null) return c;
            c.Name = r.Str("name");
            c.Order = r.Str("order");
            c.Firmware = r.Str("firmware");
            c.Address = r.Str("address");
            c.SubnetMask = r.Str("subnetMask");
            c.Router = r.Str("router");
            c.ProfinetName = r.Str("profinetName");
            return c;
        }

        public Obj ToJson()
        {
            var o = new Obj();
            o["name"] = Name;
            o["order"] = Order;
            o["firmware"] = Firmware;
            o["address"] = Address;
            o["subnetMask"] = SubnetMask;
            if (Router != "") o["router"] = Router;
            o["profinetName"] = ProfinetName;
            return o;
        }
    }

    public sealed class WebUser
    {
        public string Name = "";
        public List<string> Rights = new List<string>();
    }

    public sealed class WebServer
    {
        public bool Enabled;
        public List<WebUser> Users = new List<WebUser>();

        public Obj ToJson()
        {
            var o = new Obj();
            o["enabled"] = Enabled;
            if (Users.Count > 0)
            {
                var us = new List<object>();
                foreach (var u in Users)
                {
                    var x = new Obj();
                    x["name"] = u.Name;
                    x["rights"] = u.Rights.ToArray();
                    us.Add(x);
                }
                o["users"] = us;
            }
            return o;
        }
    }

    public sealed class Module
    {
        public int Slot;
        public string Order = "", Firmware = "";
        public int? InputStart, OutputStart;

        public Obj ToJson()
        {
            var o = new Obj();
            o["slot"] = Slot;
            o["order"] = Order;
            if (Firmware != "") o["firmware"] = Firmware;
            if (InputStart.HasValue) o["inputStart"] = InputStart.Value;
            if (OutputStart.HasValue) o["outputStart"] = OutputStart.Value;
            return o;
        }
    }

    public sealed class DeviceModule
    {
        public int Slot, Subslot;
        public string Module = "";
        public int? InputStart, OutputStart;

        public Obj ToJson()
        {
            var o = new Obj();
            o["slot"] = Slot;
            o["subslot"] = Subslot;
            o["module"] = Module;
            if (InputStart.HasValue) o["inputStart"] = InputStart.Value;
            if (OutputStart.HasValue) o["outputStart"] = OutputStart.Value;
            return o;
        }
    }

    public sealed class Device
    {
        public string Gsdml = "", Dap = "", Name = "", Address = "", UpdateTime = "";
        public List<DeviceModule> Modules = new List<DeviceModule>();

        public Obj ToJson()
        {
            var o = new Obj();
            o["gsdml"] = Gsdml;
            o["dap"] = Dap;
            o["name"] = Name;
            o["address"] = Address;
            if (UpdateTime != "") o["updateTime"] = UpdateTime;
            if (Modules.Count > 0)
            {
                var ms = new List<object>();
                foreach (var m in Modules) ms.Add(m.ToJson());
                o["modules"] = ms;
            }
            return o;
        }
    }

    public sealed class Profinet
    {
        public string UpdateTime = "";
        public List<Device> Devices = new List<Device>();

        public Obj ToJson()
        {
            var o = new Obj();
            if (UpdateTime != "") o["updateTime"] = UpdateTime;
            if (Devices.Count > 0)
            {
                var ds = new List<object>();
                foreach (var d in Devices) ds.Add(d.ToJson());
                o["devices"] = ds;
            }
            return o;
        }
    }

    // Secrets are the passwords a call needs. They are never logged and
    // never written to the work dir.
    public sealed class Secrets
    {
        public Dictionary<string, string> WebUsers = new Dictionary<string, string>(StringComparer.Ordinal);
        public string Protection = "";

        public static Secrets Read(Reader r)
        {
            var s = new Secrets();
            if (r == null) return s;
            s.WebUsers = r.StrMap("webUsers");
            s.Protection = r.Str("protection");
            return s;
        }
    }

    public sealed class Station
    {
        public Cpu Cpu = new Cpu();
        public WebServer WebServer;
        public List<Module> Modules = new List<Module>();
        public Profinet Profinet;

        // Read reads a station strictly; the caller's Done on the root
        // refuses unknown fields at every depth.
        public static Station Read(Reader r)
        {
            var s = new Station();
            if (r == null) return s;
            s.Cpu = Cpu.Read(r.Child("cpu"));
            var w = r.Child("webServer");
            if (w != null)
            {
                s.WebServer = new WebServer { Enabled = w.Bool("enabled") };
                foreach (var u in w.Children("users"))
                {
                    s.WebServer.Users.Add(new WebUser { Name = u.Str("name"), Rights = u.Strs("rights") });
                }
            }
            foreach (var m in r.Children("modules"))
            {
                s.Modules.Add(new Module
                {
                    Slot = m.Int("slot"), Order = m.Str("order"), Firmware = m.Str("firmware"),
                    InputStart = m.OptInt("inputStart"), OutputStart = m.OptInt("outputStart"),
                });
            }
            var p = r.Child("profinet");
            if (p != null)
            {
                s.Profinet = new Profinet { UpdateTime = p.Str("updateTime") };
                foreach (var d in p.Children("devices"))
                {
                    var dev = new Device
                    {
                        Gsdml = d.Str("gsdml"), Dap = d.Str("dap"), Name = d.Str("name"),
                        Address = d.Str("address"), UpdateTime = d.Str("updateTime"),
                    };
                    foreach (var m in d.Children("modules"))
                    {
                        dev.Modules.Add(new DeviceModule
                        {
                            Slot = m.Int("slot"), Subslot = m.Int("subslot"), Module = m.Str("module"),
                            InputStart = m.OptInt("inputStart"), OutputStart = m.OptInt("outputStart"),
                        });
                    }
                    s.Profinet.Devices.Add(dev);
                }
            }
            return s;
        }

        public Obj ToJson()
        {
            var o = new Obj();
            o["cpu"] = Cpu.ToJson();
            if (WebServer != null) o["webServer"] = WebServer.ToJson();
            if (Modules.Count > 0)
            {
                var ms = new List<object>();
                foreach (var m in Modules) ms.Add(m.ToJson());
                o["modules"] = ms;
            }
            if (Profinet != null) o["profinet"] = Profinet.ToJson();
            return o;
        }

        static string I(int v)
        {
            return v.ToString(CultureInfo.InvariantCulture);
        }

        // Validate returns every rule the station breaks, as Go's
        // tiad.Station.Validate words it, or null.
        public string Validate()
        {
            var errs = new List<string>();
            var c = Cpu;
            if (c.Name == "") errs.Add("cpu.name is empty");
            if (c.Order == "") errs.Add("cpu.order is empty");
            if (c.Firmware == "") errs.Add("cpu.firmware is empty");
            if (!Ip.IsIPv4(c.Address)) errs.Add("cpu.address " + Json.Quote(c.Address) + " is not an IPv4 address");
            if (!Ip.IsIPv4(c.SubnetMask)) errs.Add("cpu.subnetMask " + Json.Quote(c.SubnetMask) + " is not an IPv4 mask");
            if (c.Router != "" && !Ip.IsIPv4(c.Router)) errs.Add("cpu.router " + Json.Quote(c.Router) + " is not an IPv4 address");
            string e = StationRules.CheckStationName(c.ProfinetName);
            if (e != null) errs.Add("cpu.profinetName: " + e);

            if (WebServer != null)
            {
                var seen = new HashSet<string>(StringComparer.Ordinal);
                for (int i = 0; i < WebServer.Users.Count; i++)
                {
                    var u = WebServer.Users[i];
                    if (u.Name == "") errs.Add("webServer.users[" + I(i) + "].name is empty");
                    if (seen.Contains(u.Name)) errs.Add("webServer.users: " + Json.Quote(u.Name) + " twice");
                    seen.Add(u.Name);
                    foreach (var r in u.Rights)
                    {
                        if (!StationRules.KnownRights.Contains(r))
                        {
                            errs.Add("webServer.users[" + I(i) + "]: unknown right " + Json.Quote(r) + " (one of read, write, mode, diagnostics)");
                        }
                    }
                }
            }

            var slots = new HashSet<int>();
            for (int i = 0; i < Modules.Count; i++)
            {
                var m = Modules[i];
                if (m.Slot < 0 || m.Slot == 1)
                {
                    errs.Add("modules[" + I(i) + "].slot " + I(m.Slot) + ": slot 1 is the CPU's own; modules go in slot 2 and up");
                }
                if (slots.Contains(m.Slot)) errs.Add("modules: slot " + I(m.Slot) + " twice");
                slots.Add(m.Slot);
                if (m.Order == "") errs.Add("modules[" + I(i) + "].order is empty");
                CheckStart(errs, "modules[" + I(i) + "]", m.InputStart, m.OutputStart);
            }

            if (Profinet != null)
            {
                if (Profinet.UpdateTime != "")
                {
                    e = StationRules.CheckUpdateTime(Profinet.UpdateTime);
                    if (e != null) errs.Add("profinet.updateTime: " + e);
                }
                var names = new HashSet<string>(StringComparer.Ordinal) { c.ProfinetName };
                var addrs = new HashSet<string>(StringComparer.Ordinal) { c.Address };
                for (int i = 0; i < Profinet.Devices.Count; i++)
                {
                    var d = Profinet.Devices[i];
                    string at = "profinet.devices[" + I(i) + "]";
                    if (d.Gsdml == "") errs.Add(at + ".gsdml is empty");
                    if (d.Dap == "") errs.Add(at + ".dap is empty");
                    e = StationRules.CheckStationName(d.Name);
                    if (e != null) errs.Add(at + ".name: " + e);
                    else if (names.Contains(d.Name)) errs.Add(at + ".name " + Json.Quote(d.Name) + " is already used in this station");
                    names.Add(d.Name);
                    if (!Ip.IsIPv4(d.Address)) errs.Add(at + ".address " + Json.Quote(d.Address) + " is not an IPv4 address");
                    else if (addrs.Contains(d.Address)) errs.Add(at + ".address " + d.Address + " is already used in this station");
                    addrs.Add(d.Address);
                    if (d.UpdateTime != "")
                    {
                        e = StationRules.CheckUpdateTime(d.UpdateTime);
                        if (e != null) errs.Add(at + ".updateTime: " + e);
                    }
                    var sub = new HashSet<string>(StringComparer.Ordinal);
                    for (int j = 0; j < d.Modules.Count; j++)
                    {
                        var m = d.Modules[j];
                        string mat = at + ".modules[" + I(j) + "]";
                        if (m.Slot < 0 || m.Subslot < 1)
                        {
                            errs.Add(mat + ": slot " + I(m.Slot) + " subslot " + I(m.Subslot) + " is out of range");
                        }
                        string k = I(m.Slot) + "/" + I(m.Subslot);
                        if (sub.Contains(k)) errs.Add(mat + ": slot " + I(m.Slot) + " subslot " + I(m.Subslot) + " twice");
                        sub.Add(k);
                        if (m.Module == "") errs.Add(mat + ".module is empty");
                        CheckStart(errs, mat, m.InputStart, m.OutputStart);
                    }
                }
            }
            return errs.Count == 0 ? null : "station: " + string.Join("; ", errs);
        }

        static void CheckStart(List<string> errs, string at, int? input, int? output)
        {
            if (input.HasValue && input.Value < 0) errs.Add(at + ".inputStart " + I(input.Value) + " is negative");
            if (output.HasValue && output.Value < 0) errs.Add(at + ".outputStart " + I(output.Value) + " is negative");
        }

        static bool Same(object a, object b)
        {
            return Json.Write(a) == Json.Write(b);
        }

        // Changes is tiadtest.StationChanges: in words, what applying next
        // over prev changes. tiad-fake and tiad.Openness both report it.
        public static List<string> Changes(Station prev, Station next)
        {
            var outp = new List<string>();
            if (prev == null)
            {
                outp.Add("station created");
                return outp;
            }
            if (!Same(prev.Cpu.ToJson(), next.Cpu.ToJson())) outp.Add("cpu " + next.Cpu.Name + " changed");
            if (!Same(prev.WebServer == null ? null : prev.WebServer.ToJson(), next.WebServer == null ? null : next.WebServer.ToJson()))
            {
                outp.Add("web server changed");
            }
            var pm = new SortedDictionary<int, Module>();
            foreach (var m in prev.Modules) pm[m.Slot] = m;
            var nm = new SortedDictionary<int, Module>();
            foreach (var m in next.Modules) nm[m.Slot] = m;
            var slots = new SortedSet<int>(pm.Keys);
            slots.UnionWith(nm.Keys);
            foreach (int slot in slots)
            {
                Module a, b;
                bool inA = pm.TryGetValue(slot, out a), inB = nm.TryGetValue(slot, out b);
                string s = slot.ToString(System.Globalization.CultureInfo.InvariantCulture);
                if (!inA) outp.Add("module slot " + s + " added");
                else if (!inB) outp.Add("module slot " + s + " removed");
                else if (!Same(a.ToJson(), b.ToJson())) outp.Add("module slot " + s + " changed");
            }
            var pd = Devices(prev.Profinet);
            var nd = Devices(next.Profinet);
            var names = new SortedSet<string>(pd.Keys, StringComparer.Ordinal);
            names.UnionWith(nd.Keys);
            foreach (string n in names)
            {
                Device a, b;
                bool inA = pd.TryGetValue(n, out a), inB = nd.TryGetValue(n, out b);
                if (!inA) outp.Add("device " + n + " added");
                else if (!inB) outp.Add("device " + n + " removed");
                else if (!Same(a.ToJson(), b.ToJson())) outp.Add("device " + n + " changed");
            }
            if ((prev.Profinet == null) != (next.Profinet == null) ||
                prev.Profinet != null && next.Profinet != null && prev.Profinet.UpdateTime != next.Profinet.UpdateTime)
            {
                outp.Add("IO system changed");
            }
            return outp;
        }

        static Dictionary<string, Device> Devices(Profinet p)
        {
            var m = new Dictionary<string, Device>(StringComparer.Ordinal);
            if (p != null)
            {
                foreach (var d in p.Devices) m[d.Name] = d;
            }
            return m;
        }
    }

    // StationRules are the siemens/station primitives.
    public static class StationRules
    {
        public static readonly HashSet<string> KnownRights = new HashSet<string>(StringComparer.Ordinal)
        {
            "read", "write", "mode", "diagnostics",
        };

        public const long MinUpdateTime = 16000000L; // 16ms in nanoseconds

        // CheckUpdateTime returns why s is not a usable update time, or null.
        public static string CheckUpdateTime(string s)
        {
            long d;
            if (!GoDuration.TryParse(s, out d)) return Json.Quote(s) + " is not a duration";
            if (d < MinUpdateTime)
            {
                return GoDuration.Format(d) + " is below the " + GoDuration.Format(MinUpdateTime) + " minimum";
            }
            return null;
        }

        // CheckStationName applies the PROFINET name-of-station rules and
        // returns why n breaks them, or null. Lengths are in UTF-8 bytes,
        // as Go counts them.
        public static string CheckStationName(string n)
        {
            if (n == "") return "empty (names are always explicit)";
            if (Encoding.UTF8.GetByteCount(n) > 240) return Json.Quote(n) + " is longer than 240 characters";
            string[] labels = n.Split('.');
            foreach (string label in labels)
            {
                if (label == "" || Encoding.UTF8.GetByteCount(label) > 63)
                {
                    return Json.Quote(n) + " has a label that is empty or longer than 63 characters";
                }
                if (label[0] == '-' || label[label.Length - 1] == '-')
                {
                    return Json.Quote(n) + " has a label starting or ending with a hyphen";
                }
                foreach (char r in label)
                {
                    if (!(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-'))
                    {
                        return Json.Quote(n) + ": only lowercase letters, digits, hyphens and dots are allowed";
                    }
                }
            }
            if (IpShaped(labels)) return Json.Quote(n) + " has the form n.n.n.n (n = 0-999), which PROFINET forbids";
            if (ReservedPort(labels[0]))
            {
                return Json.Quote(n) + ": a first label of the form port-xyz or port-xyz-abcde is reserved by PROFINET";
            }
            return null;
        }

        static bool AllDigits(string s, int from, int count)
        {
            if (from + count != s.Length && count != -1) return false;
            for (int i = from; i < s.Length; i++)
            {
                if (s[i] < '0' || s[i] > '9') return false;
            }
            return true;
        }

        // ReservedPort is ^port-[0-9]{3}(-[0-9]{5})?$ without a regex, so
        // no Unicode digit class can creep in.
        static bool ReservedPort(string l)
        {
            if (!l.StartsWith("port-", StringComparison.Ordinal)) return false;
            if (l.Length == 8) return AllDigits(l, 5, 3);
            if (l.Length == 14 && l[8] == '-') return AllDigits(l.Substring(0, 8), 5, 3) && AllDigits(l, 9, 5);
            return false;
        }

        static bool IpShaped(string[] labels)
        {
            if (labels.Length != 4) return false;
            foreach (string l in labels)
            {
                if (l.Length < 1 || l.Length > 3) return false;
                foreach (char r in l)
                {
                    if (r < '0' || r > '9') return false;
                }
            }
            return true;
        }
    }
}
