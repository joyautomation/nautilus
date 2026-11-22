// Web.cs: the CPU's web server (webServer, siemens-ide.md section 3.3,
// #789 stage 5 (A) step 2). tiad turns it on only on a CPU whose access
// control is on: with access control off, a FW 3.1 CPU's web server knows
// one user, "Anonymous", with full rights over the CPU (S7-1500 Web server
// manual, 11/2023), so anyone who reaches it could write tags and stop the
// CPU. Web users wait for step 2B.

using System.Collections.Generic;

namespace Tiad
{
    // IWebServer is the CPU's web server as TIA's Openness offers it: the
    // CPU's WebserverActivate and WebserverHttpsOnly, and its PROFINET
    // interface's UseWebServerForInterface. tiad.Openness wraps TIA's; the
    // fakes model it.
    public interface IWebServer
    {
        // Server is the CPU's WebserverActivate.
        bool Server();
        // Interface is UseWebServerForInterface on the interface that
        // carries the CPU's address.
        bool Interface();
        // HttpsOnly is the CPU's WebserverHttpsOnly (TIA offers it only
        // while the server is on).
        bool HttpsOnly();
        // Set writes the switches that differ: on turns the server on,
        // HTTPS only, then the interface; off turns the interface off,
        // then the server.
        void Set(bool on);
    }

    // WebRule decides whether the web server may be on.
    public static class WebRule
    {
        // Refusal is why a web server cannot be turned on over access
        // control in state (PlcAccessControlConfiguration's name), or null:
        // only EnabledWithAccessControl lets it. Absent (a CPU without the
        // setting) and any other state refuse.
        public static string Refusal(string state)
        {
            if (state == AccessRule.Enabled) return null;
            string what = state == AccessRule.Absent ? "not available (no such setting)" : "set to " + Json.Quote(state);
            return "webServer.enabled is true, and the CPU's access control is " + what +
                ": with access control off, a FW 3.1 CPU's web server lets anyone in as \"Anonymous\" with full rights;" +
                " turn access control on (a CPU tiad makes has it on), or set webServer.enabled to false";
        }

        // Plan refuses, before any write, a web server the apply would turn
        // on over a kept CPU's access control. current is the kept CPU's
        // state, or null for a CPU the apply makes (WebSteps.Run checks
        // that one once its access control is applied). Turning the server
        // off, or leaving it as found, is never refused.
        public static void Plan(string current, WebServer want)
        {
            if (want == null || !want.Enabled || current == null) return;
            string why = Refusal(current);
            if (why != null) throw TiadException.BadRequest("{0}", why);
        }
    }

    // WebSteps applies webServer in the station step, after
    // AccessSteps.Run.
    public static class WebSteps
    {
        // Run makes the web server what want says and reads it back. want
        // null leaves a kept CPU's web server as found and turns a new
        // CPU's off. Turning it on reads the CPU's access control first,
        // as AccessSteps left it, and refuses anything but
        // EnabledWithAccessControl before writing.
        public static void Run(IWebServer w, IAccessControl c, WebServer want, bool madeCpu, List<string> notes)
        {
            if (want == null && !madeCpu)
            {
                // As found, but never silently: a kept CPU serving the web
                // over access control off serves "Anonymous" with full
                // rights, and a download would load that.
                if (w.Server() && WebRule.Refusal(c.State()) != null)
                {
                    Log.Line("WARNING web server: the kept CPU's web server is on over access control {0}, left as found (webServer left out)", Json.Quote(c.State()));
                    notes.Add(AnonymousNote);
                }
                return;
            }
            bool on = want != null && want.Enabled;
            if (on)
            {
                string why = WebRule.Refusal(c.State());
                if (why != null) throw new TiaException(why);
            }
            w.Set(on);
            bool server = w.Server(), iface = w.Interface();
            if (server != on || iface != on)
            {
                throw new TiaException("after turning the web server " + (on ? "on" : "off") + " the CPU reads WebserverActivate " +
                    (server ? "true" : "false") + " and UseWebServerForInterface " + (iface ? "true" : "false"));
            }
            // Every CPU tiad serves a web server on has a user with a
            // password (the Full access user at least): never over plain
            // HTTP.
            if (on && !w.HttpsOnly()) throw new TiaException("after turning the web server on the CPU reads WebserverHttpsOnly false");
            Log.Line("web server: {0}", on ? "on" : "off");
            if (on) notes.Add(OnNote);
        }

        public const string AnonymousNote = "WARNING: the kept CPU's web server is on and its access control is not on, so it lets anyone in as \"Anonymous\" with full rights; webServer is left out, so tiad leaves it as found, and a download loads it so: set webServer.enabled to false to turn it off";

        public const string OnNote = "the web server is on, HTTPS only, over access control EnabledWithAccessControl: a web client may do what the runtime rights of its user's roles on this CPU allow";
    }
}
