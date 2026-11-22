// WebTest.cs: the CPU's web server (core/Web.cs): it comes on only over
// access control EnabledWithAccessControl, refused before any write
// otherwise; turning it off, or leaving it out, is never refused; and what
// was written is read back.

using System.Collections.Generic;

namespace Tiad.Test
{
    static partial class Program
    {
        // ScriptedWeb records every write; IgnoreServer and IgnoreInterface
        // make a write leave that switch as it was.
        sealed class ScriptedWeb : IWebServer
        {
            public bool On, Iface, IgnoreServer, IgnoreInterface;
            public List<string> Calls = new List<string>();

            public bool Server()
            {
                return On;
            }

            public bool Interface()
            {
                return Iface;
            }

            public void Set(bool on)
            {
                Calls.Add(on ? "on" : "off");
                if (!IgnoreServer) On = on;
                if (!IgnoreInterface) Iface = on;
            }
        }

        static readonly string[] NotEnabled = { "Disabled", AccessRule.Absent, "EnabledWithAccessControlViaAccessLevel", "enabledwithaccesscontrol" };

        // WebPlan: a kept CPU's access control must read exactly
        // EnabledWithAccessControl for the server to come on; a CPU the
        // apply makes is checked later (Run); off and absent never refuse.
        static void WebPlan()
        {
            var on = new WebServer { Enabled = true };
            Eq(WebRule.Refusal(AccessRule.Enabled), null, "access control on");
            WebRule.Plan(AccessRule.Enabled, on);
            WebRule.Plan(null, on);
            foreach (string st in NotEnabled)
            {
                WebRule.Plan(st, null);
                WebRule.Plan(st, new WebServer { Enabled = false });
                var e = Throws<TiadException>(delegate { WebRule.Plan(st, on); }, "on over " + Show(st));
                if (e == null) continue;
                Eq(e.Status, 400, st + " status");
                Eq(e.Message, WebRule.Refusal(st), st + " message");
                Contains(e.Message, "\"Anonymous\" with full rights", st + " names the hazard");
                Contains(e.Message, "set webServer.enabled to false", st + " names the fix");
            }
            Contains(WebRule.Refusal(AccessRule.Absent), "access control is not available (no such setting)", "absent");
            Contains(WebRule.Refusal("Disabled"), "access control is set to \"Disabled\"", "disabled");
        }

        // WebSteps: on reads the access control as AccessSteps left it and
        // writes nothing unless it is EnabledWithAccessControl; off is
        // written over any access control; absent leaves a kept CPU alone
        // and turns a new CPU's off.
        static void WebSteps_()
        {
            var on = new WebServer { Enabled = true };
            var w = new ScriptedWeb();
            var notes = new List<string>();
            WebSteps.Run(w, new ScriptedAccess(), on, false, notes);
            True(w.On && w.Iface, "on over access control on");
            Eq(string.Join(",", w.Calls.ToArray()), "on", "one write");
            Eq(string.Join("|", notes.ToArray()), WebSteps.OnNote, "the note");

            foreach (bool made in new[] { true, false })
            {
                foreach (string st in NotEnabled)
                {
                    w = new ScriptedWeb();
                    notes = new List<string>();
                    var e = Throws<TiaException>(delegate { WebSteps.Run(w, new ScriptedAccess { State_ = st }, on, made, notes); }, "on over " + Show(st));
                    if (e != null) Eq(e.Message, WebRule.Refusal(st), st + " message");
                    Eq(w.Calls.Count, 0, st + ": nothing written");
                    Eq(notes.Count, 0, st + ": no note");

                    w = new ScriptedWeb { On = true, Iface = true };
                    WebSteps.Run(w, new ScriptedAccess { State_ = st }, new WebServer { Enabled = false }, made, notes);
                    True(!w.On && !w.Iface, st + ": off is never refused");
                }
            }

            w = new ScriptedWeb { On = true, Iface = true };
            WebSteps.Run(w, new ScriptedAccess { State_ = "Disabled" }, null, false, new List<string>());
            Eq(w.Calls.Count, 0, "absent leaves a kept CPU's server as found");
            True(w.On, "still on");
            w = new ScriptedWeb { On = true, Iface = true };
            WebSteps.Run(w, new ScriptedAccess { State_ = "Disabled" }, null, true, new List<string>());
            True(!w.On && !w.Iface, "absent turns a new CPU's server off");
        }

        // WebStepsReadBack: a switch that did not take refuses, naming both.
        static void WebStepsReadBack()
        {
            var w = new ScriptedWeb { IgnoreInterface = true };
            var e = Throws<TiaException>(delegate { WebSteps.Run(w, new ScriptedAccess(), new WebServer { Enabled = true }, true, new List<string>()); }, "interface ignored");
            if (e != null) Eq(e.Message, "after turning the web server on the CPU reads WebserverActivate true and UseWebServerForInterface false", "interface ignored");
            w = new ScriptedWeb { On = true, Iface = true, IgnoreServer = true };
            e = Throws<TiaException>(delegate { WebSteps.Run(w, new ScriptedAccess(), new WebServer { Enabled = false }, false, new List<string>()); }, "server ignored");
            if (e != null) Eq(e.Message, "after turning the web server off the CPU reads WebserverActivate true and UseWebServerForInterface false", "server ignored");
        }
    }
}
