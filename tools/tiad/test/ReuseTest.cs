// ReuseTest.cs: whether an apply reuses the project's CPU (core/Reuse.cs),
// what it keeps, and the configuration-protection steps it runs.

using System;
using System.Collections.Generic;

namespace Tiad.Test
{
    static partial class Program
    {
        static Cpu SpecCpu()
        {
            return new Cpu { Name = "PLC_1", Order = "6ES7 510-1DK03-0AB0", Firmware = "V3.1", Address = "192.0.2.10", ProfinetName = "plc-1" };
        }

        static ProjectCpu InProject()
        {
            return new ProjectCpu { Name = "PLC_1", Order = "6ES7 510-1DK03-0AB0", Firmware = "V3.1", Protection = ConfigProtection.None };
        }

        static Station SpecStation()
        {
            return new Station { Cpu = SpecCpu() };
        }

        // ReuseDecides: no CPU makes one; the same CPU is reused.
        static void ReuseDecides()
        {
            Eq(CpuReuse.Decide(null, SpecCpu(), false), CpuReuse.Create, "a project without a CPU");
            Eq(CpuReuse.Decide(InProject(), SpecCpu(), false), CpuReuse.Reuse, "the same CPU");
            var noV = SpecCpu();
            noV.Firmware = "3.1";
            Eq(CpuReuse.Decide(InProject(), noV, false), CpuReuse.Reuse, "firmware without its V, as TypeIdentifier takes it");
        }

        // ReuseRefusesAMismatch: name, order or firmware differing refuses,
        // naming each and carrying the project's CPU for the next spec.
        static void ReuseRefusesAMismatch()
        {
            var cases = new[]
            {
                new[] { "name", "PLC_2", "name \"PLC_1\", not \"PLC_2\"" },
                new[] { "order", "6ES7 510-1SK03-0AB0", "order \"6ES7 510-1DK03-0AB0\", not \"6ES7 510-1SK03-0AB0\"" },
                new[] { "firmware", "V3.0", "firmware \"V3.1\", not \"V3.0\"" },
            };
            foreach (var c in cases)
            {
                var spec = SpecCpu();
                if (c[0] == "name") spec.Name = c[1];
                if (c[0] == "order") spec.Order = c[1];
                if (c[0] == "firmware") spec.Firmware = c[1];
                var e = Throws<TiadException>(delegate { CpuReuse.Decide(InProject(), spec, false); }, c[0]);
                if (e == null) continue;
                Eq(e.Kind, Kinds.CpuMismatch, c[0] + " kind");
                Eq(e.Status, 409, c[0] + " status");
                Contains(e.Message, c[2], c[0]);
                Contains(e.Message, "create the project again (POST /v1/projects)", c[0] + " names the fix");
                Eq(Json.Write(e.Payload), "{\"cpu\":{\"name\":\"PLC_1\",\"order\":\"6ES7 510-1DK03-0AB0\",\"firmware\":\"V3.1\"}}", c[0] + " data");
            }
            var named = SpecCpu();
            named.Name = "plc_1";
            Throws<TiadException>(delegate { CpuReuse.Decide(InProject(), named, false); }, "a name differing in case is another name");
        }

        // ReuseNeverAfterAFailure: after an apply or upload failed
        // part-way, nothing is reused or made, even a matching CPU.
        static void ReuseNeverAfterAFailure()
        {
            foreach (var pc in new[] { InProject(), null })
            {
                string what = pc == null ? "no CPU" : "the same CPU";
                var e = Throws<TiadException>(delegate { CpuReuse.Decide(pc, SpecCpu(), true); }, what);
                if (e == null) continue;
                Eq(e.Kind, Kinds.UncleanSession, what + " kind");
                Eq(e.Status, 409, what + " status");
                Contains(e.Message, "close this session without saving", what + " names the fix");
            }
            Contains(CpuReuse.Unclean("saving it").Message, "; saving it is refused:", "the refusal names what");
        }

        // ReuseFirmwareIsExact: TIA stores the configured version; V3.1 is
        // not V3.1.3 either way, and no major.minor widening.
        static void ReuseFirmwareIsExact()
        {
            True(CpuReuse.SameFirmware("V3.1", "V3.1"), "same");
            True(CpuReuse.SameFirmware("V3.1", "3.1"), "V aside");
            True(CpuReuse.SameFirmware("3.1", "V3.1"), "V aside, other side");
            True(!CpuReuse.SameFirmware("V3.1", "V3.1.3"), "V3.1 is not V3.1.3");
            True(!CpuReuse.SameFirmware("V3.1.3", "V3.1"), "V3.1.3 is not V3.1");
            True(!CpuReuse.SameFirmware("V3.1", "V3.10"), "V3.1 is not V3.10");
            True(!CpuReuse.SameFirmware("V3.1", "v3.1"), "only an upper-case V is TIA's");
            var pc = InProject();
            pc.Firmware = "V3.1.3";
            Throws<TiadException>(delegate { CpuReuse.Decide(pc, SpecCpu(), false); }, "a V3.1 spec on a V3.1.3 CPU");
        }

        // KeptDevicesCollide: a kept device sharing a name, a PROFINET name
        // or an address with the spec refuses; one that does not is kept.
        static void KeptDevicesCollide()
        {
            Func<KeptDevice, ProjectCpu> with = delegate(KeptDevice k)
            {
                var pc = InProject();
                pc.Others.Add(new KeptDevice { Name = "io-other", ProfinetName = "io-other", Address = "192.0.2.99" });
                pc.Others.Add(k);
                return pc;
            };
            var spec = SpecStation();
            spec.Profinet = new Profinet();
            spec.Profinet.Devices.Add(new Device { Name = "io-device-1", Address = "192.0.2.20" });
            CpuReuse.Collides(with(new KeptDevice { Name = "io-up", ProfinetName = "io-up", Address = "192.0.2.30" }), spec);
            CpuReuse.Collides(with(new KeptDevice { Name = "io-up" }), spec);
            CpuReuse.Collides(InProject(), SpecStation());
            var clashes = new[]
            {
                new KeptDevice { Name = "io-device-1", Address = "192.0.2.30" },
                new KeptDevice { Name = "IO-DEVICE-1", Address = "192.0.2.30" },
                new KeptDevice { Name = "x", ProfinetName = "io-device-1", Address = "192.0.2.30" },
                new KeptDevice { Name = "x", Address = "192.0.2.20" },
                new KeptDevice { Name = "x", Address = "192.0.2.10" },
                new KeptDevice { Name = "PLC_1" },
                new KeptDevice { Name = "x", ProfinetName = "plc-1" },
            };
            foreach (var k in clashes)
            {
                string what = k.Describe();
                var e = Throws<TiadException>(delegate { CpuReuse.Collides(with(k), spec); }, what);
                if (e == null) continue;
                Eq(e.Kind, Kinds.CpuMismatch, what + " kind");
                Contains(e.Message, k.Describe(), what + " names the device");
                True(e.Message.IndexOf("io-other", StringComparison.Ordinal) < 0, what + " names only the clash");
                Contains(Json.Write(e.Payload), "\"keptDevices\":[", what + " data lists the kept devices");
                Contains(Json.Write(e.Payload), "\"cpu\":{\"name\":\"PLC_1\"", what + " data holds the cpu");
            }
            // A spec device without an address does not match a kept one
            // without an address.
            var noAddr = SpecStation();
            noAddr.Profinet = new Profinet();
            noAddr.Profinet.Devices.Add(new Device { Name = "io-a" });
            CpuReuse.Collides(with(new KeptDevice { Name = "io-b" }), noAddr);
        }

        // KeptNoteAndChanges: the note lists each kept device, quoted; the
        // changes say what an apply did.
        static void KeptNoteAndChanges()
        {
            Eq(CpuReuse.KeptNote(new List<KeptDevice>()), null, "nothing kept, no note");
            string n = CpuReuse.KeptNote(new List<KeptDevice>
            {
                new KeptDevice { Name = "io \"up\"", ProfinetName = "io-up", Address = "192.0.2.30" },
                new KeptDevice { Name = "bare" },
            });
            Contains(n, "kept as found", "the note says kept");
            Contains(n, "a download loads them too", "the note says the download takes them");
            Contains(n, "\"io \\\"up\\\"\" profinetName=\"io-up\" address=\"192.0.2.30\"; \"bare\"", "each device, quoted");
            Eq(string.Join("|", CpuReuse.Changes(CpuReuse.Create, SpecStation(), SpecStation()).ToArray()), "station created", "create");
            Eq(string.Join("|", CpuReuse.Changes(CpuReuse.Reuse, null, SpecStation()).ToArray()), "cpu PLC_1 taken over from the project", "reuse after an upload");
            Eq(CpuReuse.Changes(CpuReuse.Reuse, SpecStation(), SpecStation()).Count, 0, "reuse of the same spec");
            var moved = SpecStation();
            moved.Cpu.Address = "192.0.2.11";
            Eq(string.Join("|", CpuReuse.Changes(CpuReuse.Reuse, SpecStation(), moved).ToArray()), "cpu PLC_1 changed", "reuse with a changed address");
        }

        // KeptDevicesNeedANamedYes: a download's yes must name exactly the
        // kept set; no kept devices, no question.
        static void KeptDevicesNeedANamedYes()
        {
            var one = new List<KeptDevice> { new KeptDevice { Name = "IO1" } };
            var two = new List<KeptDevice> { new KeptDevice { Name = "IO2" }, new KeptDevice { Name = "IO1" } };
            var none = new List<KeptDevice>();
            Func<string[], List<string>> l = delegate(string[] a) { return new List<string>(a); };
            Eq(KeptRule.Check(none, l(new string[0])), null, "nothing kept, nothing asked");
            Eq(KeptRule.Check(one, l(new[] { "IO1" })), null, "the kept set named");
            Eq(KeptRule.Check(two, l(new[] { "IO1", "IO2" })), null, "order does not matter");
            Eq(KeptRule.Check(two, l(new[] { "IO2", "IO1", "IO1" })), null, "a name twice is one");
            Contains(KeptRule.Check(two, l(new string[0])), "the project keeps device(s) [\"IO1\", \"IO2\"] that a download loads too: name them in decisions.keptDevices", "unanswered");
            Contains(KeptRule.Check(two, l(new[] { "IO1" })), "decisions.keptDevices names [\"IO1\"], but the project keeps [\"IO1\", \"IO2\"] now", "a yes for a smaller set");
            Contains(KeptRule.Check(one, l(new[] { "IO1", "IO2" })), "but the project keeps [\"IO1\"] now", "a yes for a larger set");
            Contains(KeptRule.Check(none, l(new[] { "IO1" })), "but the project keeps none now", "a yes when nothing is kept");
            True(KeptRule.Check(one, l(new[] { "io1" })) != null, "names compare exactly");
            Question q = KeptRule.Question(two);
            Eq(q.Id, QuestionIds.KeptDevices, "question id");
            Eq(string.Join(",", q.Objects.ToArray()), "IO1,IO2", "the question names the kept set, sorted");
        }

        // ScriptedSecret is a configurator that behaves like TIA's and
        // fails where a test says, recording every call.
        sealed class ScriptedSecret : IProtectionConfigurator
        {
            public string State_ = ConfigProtection.WithoutPassword, With = "";
            public List<string> Calls = new List<string>();
            public string FailOn = "";      // "protect", "unprotect", "remove"
            public string ProtectLeaves;    // the state a Protect leaves, when not WithPassword
            public string UnprotectLeaves;  // the state an Unprotect leaves, when not None

            public string State()
            {
                return State_;
            }

            public void Protect(string password)
            {
                Calls.Add("protect");
                if (FailOn == "protect") throw new InvalidOperationException("TIA refused protect");
                if (State_ == ConfigProtection.WithPassword) throw new InvalidOperationException("already configured");
                State_ = ProtectLeaves ?? ConfigProtection.WithPassword;
                With = password;
            }

            public void Unprotect(string password)
            {
                Calls.Add("unprotect");
                if (FailOn == "unprotect") throw new InvalidOperationException("TIA refused unprotect");
                if (password != With) throw new InvalidOperationException("wrong password");
                State_ = UnprotectLeaves ?? ConfigProtection.None;
                With = "";
            }

            public void RemoveCpu()
            {
                Calls.Add("remove");
                if (FailOn == "remove") throw new InvalidOperationException("locked");
                State_ = ConfigProtection.Absent;
            }
        }

        // ProtectionStepsHappyPaths: each action runs exactly its steps and
        // ends in the promised state.
        static void ProtectionStepsHappyPaths()
        {
            var notes = new List<string>();
            var c = new ScriptedSecret();
            Eq(ProtectionSteps.Run(c, StationRules.ProtectPassword, "Secret-pass-1", notes), ConfigProtection.Protect, "password on a new CPU");
            Eq(string.Join(",", c.Calls.ToArray()), "protect", "password: one protect");
            Eq(c.With, "Secret-pass-1", "protected with the secret");
            Eq(c.State_, ConfigProtection.WithPassword, "password: protected");
            Eq(notes.Count, 0, "no note");

            c.Calls.Clear();
            Eq(ProtectionSteps.Run(c, StationRules.ProtectPassword, "Other-pass-22", notes), ConfigProtection.Nothing, "password on a protected CPU");
            Eq(c.Calls.Count, 0, "nothing called on a protected CPU");
            Eq(notes.Count, 1, "the kept password is noted");

            Eq(ProtectionSteps.Run(c, StationRules.ProtectNone, "Secret-pass-1", notes), ConfigProtection.UnprotectWithPassword, "none on a protected CPU");
            Eq(c.State_, ConfigProtection.None, "none: off");

            var fresh = new ScriptedSecret();
            Eq(ProtectionSteps.Run(fresh, StationRules.ProtectNone, "", new List<string>()), ConfigProtection.ProtectThenUnprotect, "none on a new CPU");
            Eq(string.Join(",", fresh.Calls.ToArray()), "protect,unprotect", "none on a new CPU: two proven steps, no removal");
            Eq(fresh.State_, ConfigProtection.None, "none on a new CPU: off");
            True(fresh.With == "", "the throwaway is gone");

            var kept = new ScriptedSecret { State_ = ConfigProtection.None };
            Eq(ProtectionSteps.Run(kept, StationRules.ProtectKeep, "", new List<string>()), ConfigProtection.Nothing, "keep");
            Eq(kept.Calls.Count, 0, "keep calls nothing");

            var wrong = new ScriptedSecret { State_ = ConfigProtection.WithPassword, With = "Right-pass-1" };
            Throws<InvalidOperationException>(delegate { ProtectionSteps.Run(wrong, StationRules.ProtectNone, "Wrong-pass-1", new List<string>()); }, "a wrong password is TIA's refusal");
            Eq(string.Join(",", wrong.Calls.ToArray()), "unprotect", "a wrong password removes nothing");
        }

        // ProtectionStepsStranded: once the throwaway Protect is called,
        // any failure removes the CPU, and says whether that worked.
        static void ProtectionStepsStranded()
        {
            var cases = new[]
            {
                new[] { "protect", "", "TIA refused protect", "protect,remove" },
                new[] { "unprotect", "", "TIA refused unprotect", "protect,unprotect,remove" },
                new[] { "remove", "unprotect-fails-too", "removing the CPU from the project failed too (locked)", "protect,unprotect,remove" },
            };
            foreach (var k in cases)
            {
                var c = new ScriptedSecret { FailOn = k[0] };
                if (k[1] == "unprotect-fails-too") c.UnprotectLeaves = ConfigProtection.WithPassword;
                var e = Throws<TiaException>(delegate { ProtectionSteps.Run(c, StationRules.ProtectNone, "", new List<string>()); }, k[0]);
                if (e == null) continue;
                Contains(e.Message, "a throwaway password nobody holds", k[0] + " names the risk");
                Contains(e.Message, k[2], k[0]);
                Contains(e.Message, "close this session without saving", k[0] + " names the fix");
                Eq(string.Join(",", c.Calls.ToArray()), k[3], k[0] + " calls");
            }
        }

        // ProtectionStepsReadBack: a state TIA did not promise refuses, and
        // in the throwaway sequence it removes the CPU.
        static void ProtectionStepsReadBack()
        {
            var mid = new ScriptedSecret { ProtectLeaves = ConfigProtection.WithoutPassword };
            var e = Throws<TiaException>(delegate { ProtectionSteps.Run(mid, StationRules.ProtectNone, "", new List<string>()); }, "protect ignored");
            if (e != null) Contains(e.Message, "after protect the CPU's configuration protection reads WithoutPassword, not WithPassword", "protect ignored");
            Eq(string.Join(",", mid.Calls.ToArray()), "protect,remove", "protect ignored: no unprotect, removed");

            var end = new ScriptedSecret { UnprotectLeaves = ConfigProtection.WithPassword };
            e = Throws<TiaException>(delegate { ProtectionSteps.Run(end, StationRules.ProtectNone, "", new List<string>()); }, "unprotect ignored");
            if (e != null) Contains(e.Message, "reads WithPassword, not None", "unprotect ignored");
            Eq(string.Join(",", end.Calls.ToArray()), "protect,unprotect,remove", "unprotect ignored: removed");

            var pw = new ScriptedSecret { ProtectLeaves = ConfigProtection.None };
            e = Throws<TiaException>(delegate { ProtectionSteps.Run(pw, StationRules.ProtectPassword, "Secret-pass-1", new List<string>()); }, "password not reached");
            if (e != null) Contains(e.Message, "after protect the CPU's configuration protection reads None, not WithPassword", "password not reached");
            Eq(string.Join(",", pw.Calls.ToArray()), "protect", "password not reached: nothing removed");

            var off = new ScriptedSecret { State_ = ConfigProtection.WithPassword, With = "Secret-pass-1", UnprotectLeaves = ConfigProtection.WithPassword };
            e = Throws<TiaException>(delegate { ProtectionSteps.Run(off, StationRules.ProtectNone, "Secret-pass-1", new List<string>()); }, "none not reached");
            if (e != null) Contains(e.Message, "reads WithPassword, not None", "none not reached");
        }
    }
}
