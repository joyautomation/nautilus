// Online.cs: hold a project online in TIA Portal's window
// (siemens-ide.md section 3.4, "online").
//
// Only an agent started with TIAD_UI=1 holds a project online, so a
// person sees TIA's own online view: the CPU's state and the project
// tree's online/offline compare. It loads nothing and writes nothing; such
// an agent refuses every download. While a session is held online nothing
// else on it goes online (compare, fingerprint, upload; a download is
// refused on such an agent anyway), and a close goes offline first.

using System;

namespace Tiad
{
    public sealed partial class Agent
    {
        // OnlineNeedsUi is an online call's refusal on an agent without
        // TIA's window.
        public const string OnlineNeedsUi = "this agent runs TIA Portal without its user interface, so there is no window to hold a project online in: start the agent with TIAD_UI=1 (install-tiad.ps1 -Ui) to go online in TIA's window; such an agent refuses every download";

        void RegisterOnline(Router r)
        {
            r.Add("POST", "/v1/sessions/{id}/online", Online);
        }

        // HeldOnlineRefuses refuses call on a session held online: TIA's
        // window shows it online, and one connection is all tiad holds.
        static void HeldOnlineRefuses(Session s, string call)
        {
            if (s.HeldOnline != "")
            {
                throw new TiadException(409, Kinds.Busy, "the session is held online to " + s.HeldOnline + " in TIA's window: take it offline first (POST /v1/sessions/" + s.Id + "/online {\"online\": false}), then " + call);
            }
        }

        object Online(Call c)
        {
            NeedTia();
            if (!cfg.Ui) throw TiadException.BadRequest("{0}", OnlineNeedsUi);
            return WithSession(c, delegate(Session s)
            {
                var r = c.Json();
                bool online = r.Bool("online");
                string password = OnlinePassword(r);
                r.Done();
                var o = new Obj();
                if (!online)
                {
                    string was = OnSession(s, delegate(ITia t)
                    {
                        string held = s.HeldOnline;
                        // Cleared only once the release returned: one that
                        // throws leaves the session held, so it is retried.
                        if (held != "") t.ReleaseOnline(s.Handle);
                        s.HeldOnline = "";
                        return held;
                    });
                    if (was != "") Log.Line("audit online session={0} project={1} address={2} offline", s.Id, Json.Quote(s.Project), Json.Quote(was));
                    o["online"] = false;
                    o["address"] = was;
                    return o;
                }
                // The CPU as a compare finds it: the spec's, or the
                // project's own for a project opened as found.
                string address = s.Address;
                if (address == "")
                {
                    address = OnSession(s, delegate(ITia t) { return t.ProjectCpuAddress(s.Handle); });
                    if (address == "") throw TiadException.BadRequest("the project has no station spec, and its CPU no address to go online to");
                }
                if (!Target(address)) throw new TiadException(403, Kinds.TargetRefused, address + " is not in TIAD_TARGETS");
                CheckDevicePin(s, address, "online");
                string state = OnSession(s, delegate(ITia t)
                {
                    if (s.HeldOnline == address) return "Online";
                    if (s.HeldOnline != "") throw new TiadException(409, Kinds.Busy, "the session is held online to " + s.HeldOnline + ", not " + address + ": take it offline first");
                    // No blind trust rides along: tiad never sends a
                    // password over one, and holds no trusted connection.
                    if (blindTrust != null) throw TiadException.BadRequest("{0}", OnlineLogin.BlockedBy(blindTrust) + "; nothing went online");
                    var use = new LoginUse(LoginFor(t, password, null, address, "online"), "online", address);
                    string st = t.HoldOnline(s.Handle, address, use);
                    s.HeldOnline = address;
                    return st;
                });
                Log.Line("audit online session={0} project={1} address={2} online state={3}", s.Id, Json.Quote(s.Project), Json.Quote(address), state);
                o["online"] = true;
                o["address"] = address;
                o["state"] = state;
                return o;
            });
        }
    }
}
