// InstanceLogon.cs: the evidence the instance-logon route rests on
// (UserManagementRule, siemens-ide.md section 3.5; #789 P1 fallback).
//
// TIA may keep a logon for the life of its instance and ask no credentials
// in a later download, or ask its user-management check before
// credentials. Either way the download's own logon (the logon route)
// cannot show tiad holds the CPU's Full access when TIA asks the check.
// The instance-logon route lets the reset then on earlier evidence from
// the same TIA instance: a compare to the same address that logged on as
// "nautilus" with the same password and came back equal, with nothing
// since that could have changed or contradicted it.

using System;
using System.Collections.Generic;
using System.Security.Cryptography;
using System.Text;

namespace Tiad
{
    // InstanceLogons holds, per CPU address, the compare that last logged
    // on to it in a TIA instance: the instance (its ITia, by reference),
    // when, which call and session, and the password only as an HMAC under
    // a random key made for that record, both in memory only, never
    // written or logged. Holds is true only for the same instance, the
    // same address and the same password. A record goes when anything
    // could have changed what it shows (Forget, ForgetAll): any blind
    // trust, a retired instance, a download to the address that may have
    // acted, an operation to it whose credentials were refused or asked
    // again. The agent's restart forgets everything.
    public sealed class InstanceLogons
    {
        sealed class Entry
        {
            public object Instance;
            public byte[] Key, Mac;
            public string Since, Call, Session;
        }

        readonly object mu = new object();
        readonly Dictionary<string, Entry> byAddress = new Dictionary<string, Entry>(StringComparer.Ordinal);

        // Record notes that call (on session) compared address in
        // instance, logged on as "nautilus" with password and came back
        // equal. It replaces an earlier record for the address.
        public void Record(object instance, string address, string password, string call, string session)
        {
            if (instance == null || string.IsNullOrEmpty(address) || string.IsNullOrEmpty(password)) return;
            var e = new Entry
            {
                Instance = instance,
                Key = new byte[32],
                Since = DateTime.UtcNow.ToString("yyyy-MM-ddTHH:mm:ss.fffZ", System.Globalization.CultureInfo.InvariantCulture),
                Call = call ?? "",
                Session = session ?? "",
            };
            using (var r = new RNGCryptoServiceProvider()) r.GetBytes(e.Key);
            e.Mac = Mac(e.Key, password);
            lock (mu) byAddress[address] = e;
            Log.Line("audit instance-logon recorded call={0} session={1} address={2}: a compare logged on as {3} and came back equal in this TIA instance",
                e.Call, e.Session, Json.Quote(address), Json.Quote(OnlineLogin.User));
        }

        // Holds is whether instance holds a record for address of
        // password. The MAC is compared in fixed time.
        public bool Holds(object instance, string address, string password)
        {
            if (instance == null || string.IsNullOrEmpty(address) || string.IsNullOrEmpty(password)) return false;
            Entry e;
            lock (mu)
            {
                if (!byAddress.TryGetValue(address, out e)) return false;
            }
            if (!ReferenceEquals(e.Instance, instance)) return false;
            byte[] got = Mac(e.Key, password);
            int diff = got.Length ^ e.Mac.Length;
            for (int i = 0; i < got.Length && i < e.Mac.Length; i++) diff |= got[i] ^ e.Mac[i];
            return diff == 0;
        }

        // Describe is the record for address for an audit line ("none"
        // without one): when, which call and session, never the password.
        public string Describe(string address)
        {
            Entry e;
            lock (mu)
            {
                if (!byAddress.TryGetValue(address ?? "", out e)) return "none";
            }
            return "since=" + e.Since + " call=" + e.Call + " session=" + e.Session;
        }

        // Forget drops the record for address, logging why when there was
        // one.
        public void Forget(string address, string why)
        {
            bool had;
            lock (mu) had = byAddress.Remove(address ?? "");
            if (had) Log.Line("audit instance-logon forgotten address={0}: {1}", Json.Quote(address), why);
        }

        // ForgetAll drops every record, logging why when there was one.
        public void ForgetAll(string why)
        {
            int n;
            lock (mu)
            {
                n = byAddress.Count;
                byAddress.Clear();
            }
            if (n > 0) Log.Line("audit instance-logon forgotten all ({0}): {1}", n, why);
        }

        static byte[] Mac(byte[] key, string password)
        {
            using (var h = new HMACSHA256(key)) return h.ComputeHash(Encoding.UTF8.GetBytes(password));
        }
    }
}
