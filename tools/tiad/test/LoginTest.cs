// LoginTest.cs: what tiad answers when TIA asks credentials going online
// (OnlineLogin, LoginUse in core/TiaRules.cs; #789 stage 5 (A)).

using System;
using System.Collections.Generic;

namespace Tiad.Test
{
    static partial class Program
    {
        const string Pw = "Full-access-1";
        static readonly List<string> LoginOffered = new List<string> { "AnonymousUser", LoginUse.ProjectUser };

        // LoginFor: no password, no login; a blind trust blocks it; the
        // password never shows in its text.
        static void LoginFor()
        {
            True(OnlineLogin.For("", null) == null, "no password, no login");
            True(OnlineLogin.For(null, null) == null, "null password, no login");
            OnlineLogin l = OnlineLogin.For(Pw, null);
            Eq(l.Password, Pw, "the password");
            Eq(l.Blocked, null, "no blind trust, not blocked");
            Eq(l.ToString(), "OnlineLogin(nautilus)", "its text names the user only");
            Eq(OnlineLogin.User, "nautilus", "the user cpu.accessControl fullAccessUser makes");
            var bt = new BlindTrust { Since = "2026-11-22T00:00:00.000Z", Call = "upload", Session = "s1", Address = "192.0.2.10" };
            OnlineLogin b = OnlineLogin.For(Pw, bt);
            True(b.Blocked != null, "a blind trust blocks it");
            if (b.Blocked != null)
            {
                Contains(b.Blocked, "password not sent: this agent's TIA instance trusted an unverified CPU certificate (" + bt.Describe() + ")", "names the trust");
                Contains(b.Blocked, "close every session", "names the fix");
                True(b.Blocked.IndexOf(Pw, StringComparison.Ordinal) < 0, "never the password");
            }
        }

        // LoginDecides: every reason to answer no, each on its own.
        static void LoginDecides()
        {
            Eq(new LoginUse(OnlineLogin.For(Pw, null), "compare", "192.0.2.10").Decide(true, LoginOffered), null, "a password, secure, project users offered");
            Eq(new LoginUse(null, "compare", "192.0.2.10").Decide(true, LoginOffered), LoginUse.NoPassword, "no password");
            var bt = new BlindTrust { Since = "x", Call = "upload", Session = "s1", Address = "192.0.2.10" };
            OnlineLogin blocked = OnlineLogin.For(Pw, bt);
            Eq(new LoginUse(blocked, "compare", "192.0.2.10").Decide(true, LoginOffered), blocked.Blocked, "the instance trusts blindly");
            var t = new LoginUse(OnlineLogin.For(Pw, null), "upload", "192.0.2.10");
            t.Trusted();
            Eq(t.Decide(true, LoginOffered), LoginUse.TrustedHere, "this operation trusted blindly");
            Eq(new LoginUse(OnlineLogin.For(Pw, null), "compare", "192.0.2.10").Decide(false, LoginOffered), LoginUse.NotSecure, "not secure");
            Eq(new LoginUse(OnlineLogin.For(Pw, null), "compare", "192.0.2.10").Decide(null, LoginOffered), LoginUse.SecureUnread, "secure unreadable");
            Eq(new LoginUse(OnlineLogin.For(Pw, null), "compare", "192.0.2.10").Decide(true, null), LoginUse.TypesUnread, "user types unreadable");
            string none = new LoginUse(OnlineLogin.For(Pw, null), "compare", "192.0.2.10").Decide(true, new List<string> { "AnonymousUser", "PasswordOnly" });
            Eq(none, "the CPU offers no legitimation as a project user (it offers AnonymousUser, PasswordOnly), and tiad logs on only as the project user \"nautilus\"", "no project users");
            Contains(new LoginUse(OnlineLogin.For(Pw, null), "compare", "192.0.2.10").Decide(true, new List<string>()), "(it offers none)", "nothing offered");
            Eq(new LoginUse(OnlineLogin.For(Pw, null), "compare", "192.0.2.10").Decide(true, new List<string> { "projectuser" }),
                "the CPU offers no legitimation as a project user (it offers projectuser), and tiad logs on only as the project user \"nautilus\"", "names are exact");
            foreach (string why in new[] { LoginUse.NoPassword, LoginUse.TrustedHere, LoginUse.AskedAgain, LoginUse.RefusedBefore, LoginUse.NotSecure, LoginUse.SecureUnread, LoginUse.TypesUnread })
            {
                True(why.IndexOf(Pw, StringComparison.Ordinal) < 0, "no reason holds a password");
            }
        }

        // LoginOncePerOperation: tiad answers credentials once per
        // operation, whichever connection asks; TIA asking again may mean
        // the CPU refused the password. A question answered no earlier in
        // the operation stops any logon after it.
        static void LoginOncePerOperation()
        {
            var u = new LoginUse(OnlineLogin.For(Pw, null), "fingerprint read", "192.0.2.10");
            Eq(u.Decide(true, LoginOffered), null, "the first ask");
            True(u.Answered, "answered");
            Eq(u.Decide(true, LoginOffered), LoginUse.AskedAgain, "a second ask, on any connection");
            Eq(u.Decide(true, LoginOffered), LoginUse.AskedAgain, "and every one after it");
            // A refusal for any other reason does not count as answered.
            var v = new LoginUse(OnlineLogin.For(Pw, null), "compare", "192.0.2.10");
            Eq(v.Decide(false, LoginOffered), LoginUse.NotSecure, "not secure first");
            True(!v.Answered, "a refusal is no answer");
            // ...but it is a no in this operation: nothing follows it.
            v.Refused();
            Eq(v.Decide(true, LoginOffered), LoginUse.RefusedBefore, "after a no");
            // A record that answers no tells its operation's login.
            var w = new LoginUse(OnlineLogin.For(Pw, null), "compare", "192.0.2.10");
            var rec = new OnlineRecord { Use = w };
            Caught(delegate { rec.Handle(delegate { return "TlsVerificationConfiguration"; }, delegate(string t, OnlineRecord.Call call) { call.Refuse("trust the TLS certificate of the PLC"); }); });
            Eq(w.Decide(true, LoginOffered), LoginUse.RefusedBefore, "a certificate answered not trusted, then credentials");
            var x = new LoginUse(OnlineLogin.For(Pw, null), "compare", "192.0.2.10");
            var rec2 = new OnlineRecord { Use = x };
            Caught(delegate { rec2.Handle(delegate { throw new InvalidOperationException("unreadable"); }, delegate(string t, OnlineRecord.Call call) { }); });
            Eq(x.Decide(true, LoginOffered), LoginUse.RefusedBefore, "an unreadable question, then credentials");
            // A trust stops it too, whichever handler gave it.
            var y = new LoginUse(OnlineLogin.For(Pw, null), "upload", "192.0.2.10");
            y.Trusted();
            Eq(y.Decide(true, LoginOffered), LoginUse.TrustedHere, "after a trust");
            foreach (string why in new[] { LoginUse.TrustedHere, LoginUse.AskedAgain, LoginUse.RefusedBefore, OnlineLogin.BlockedBy(new BlindTrust { Call = "upload" }) })
            {
                True(why.StartsWith("password not sent", StringComparison.Ordinal), "a refusal after a trust or a no says the password was not sent: " + why);
            }
        }

        // DownloadNamesTheLogin: a download TIA failed after tiad answered
        // its credentials question says the CPU may have refused it.
        static void DownloadNamesTheLogin()
        {
            foreach (bool logged in new[] { false, true })
            {
                var d = new DownloadDecider(delegate(Question q) { return null; }, null);
                var e = Throws<TiaException>(delegate
                {
                    DownloadSteps.Run(d, "192.0.2.10", delegate { }, delegate { throw new InvalidOperationException("Online connection failed."); },
                        delegate { }, delegate { return new List<string>(); }, delegate { return logged; });
                }, "a download TIA failed");
                if (e != null) Eq(e.Message.Contains(DownloadSteps.LoginNote), logged, "names the logon only after one (logged on: " + logged + ")");
            }
        }

        // LoginThroughTheRecord: an answer writes the password and records
        // a login; a refusal records the question with its reason, and TIA
        // gets only Boundary.Fail's cancel.
        static void LoginThroughTheRecord()
        {
            const string type = "OnlineAuthenticationConfiguration";
            var u = new LoginUse(OnlineLogin.For(Pw, null), "compare", "192.0.2.10");
            var rec = new OnlineRecord();
            string wrote = null;
            Exception e = Caught(delegate { rec.Handle(delegate { return type; }, delegate(string t, OnlineRecord.Call call) { u.Answer(call, t, true, LoginOffered, delegate(string pw) { wrote = pw; }); }); });
            True(e == null, "an answer throws nothing back");
            Eq(wrote, Pw, "the password written");
            Eq(rec.Asked.Count, 0, "nothing answered no");
            Eq(rec.LoggedIn.Count, 1, "one login");
            if (rec.LoggedIn.Count == 1) Eq(rec.LoggedIn[0], "credentials (OnlineAuthenticationConfiguration)", "the login names the question");
            // Asked again: the CPU refused the password.
            wrote = null;
            e = Caught(delegate { rec.Handle(delegate { return type; }, delegate(string t, OnlineRecord.Call call) { u.Answer(call, t, true, LoginOffered, delegate(string pw) { wrote = pw; }); }); });
            True(e is OperationCanceledException, "TIA gets Boundary.Fail's cancel");
            Eq(wrote, null, "nothing written the second time");
            Eq(rec.Asked.Count, 1, "the second ask answered no");
            if (rec.Asked.Count == 1) Eq(rec.Asked[0], "credentials (OnlineAuthenticationConfiguration): " + LoginUse.AskedAgain, "with its reason");
            // A write that fails is a question answered no.
            var w = new LoginUse(OnlineLogin.For(Pw, null), "compare", "192.0.2.10");
            var rec2 = new OnlineRecord();
            e = Caught(delegate { rec2.Handle(delegate { return type; }, delegate(string t, OnlineRecord.Call call) { w.Answer(call, t, true, LoginOffered, delegate(string pw) { throw new ObjectDisposedException("OnlineCredentials"); }); }); });
            True(e is OperationCanceledException, "a failed write cancels");
            Eq(rec2.Asked.Count, 1, "and is answered no");
            Eq(rec2.LoggedIn.Count, 0, "and is no login");
            // No password: refused, naming the secret.
            var n = new LoginUse(null, "upload", "192.0.2.10");
            var rec3 = new OnlineRecord();
            Caught(delegate { rec3.Handle(delegate { return type; }, delegate(string t, OnlineRecord.Call call) { n.Answer(call, t, true, LoginOffered, delegate(string pw) { wrote = pw; }); }); });
            Eq(rec3.Asked.Count, 1, "no password: answered no");
            if (rec3.Asked.Count == 1) Contains(rec3.Asked[0], "secrets.fullAccessPassword", "names the secret");
        }
    }
}
