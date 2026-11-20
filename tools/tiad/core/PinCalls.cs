// PinCalls.cs: the pins calls, and the device-pin check every online call
// makes (siemens-ide.md section 3.8). None of them starts TIA except the
// check, which asks TIA's side for the CPU's identity.

using System;
using System.Collections.Generic;

namespace Tiad
{
    public sealed partial class Agent
    {
        readonly PinStore pins;
        // pinsMu makes a pin's read, compare and write one step, between a
        // re-pin and an upload's first trust.
        readonly object pinsMu = new object();

        void RegisterPins(Router r)
        {
            r.Add("GET", "/v1/pins", ListPins);
            r.Add("POST", "/v1/pins/{address}", SetPin);
        }

        static Obj PinJson(string address, DeviceIdentity d)
        {
            var o = new Obj();
            o["address"] = address;
            o["device"] = d.ToJson();
            o["holds"] = d.Holds().ToArray();
            return o;
        }

        static string Holds(DeviceIdentity d)
        {
            return "[" + string.Join(",", d.Holds().ToArray()) + "]";
        }

        object ListPins(Call c)
        {
            var l = new List<object>();
            foreach (string a in pins.Addresses())
            {
                DeviceIdentity d = pins.Device(a);
                if (d != null) l.Add(PinJson(a, d));
            }
            var o = new Obj();
            o["pins"] = l;
            return o;
        }

        // SetPin pins a CPU's identity by hand: a first pin before any
        // contact (replace false), or a re-pin after a deliberate change
        // (replace true). Both are audited with the old and new values.
        object SetPin(Call c)
        {
            string address = c.Var("address");
            if (!Ip.IsIPv4(address)) throw TiadException.BadRequest("pin: address {0} is not an IPv4 address", Json.Quote(address));
            if (!Target(address)) throw new TiadException(403, Kinds.TargetRefused, address + " is not in TIAD_TARGETS");
            var r = c.Json();
            Reader dr = r.Child("device");
            DeviceIdentity d = dr == null ? null : DeviceIdentity.Read(dr);
            bool replace = r.Bool("replace");
            bool announced = r.Bool("announced");
            r.Done();
            if (d == null) throw TiadException.BadRequest("{0}", "pin: device is required");
            string bad = d.Validate();
            if (bad != null) throw TiadException.BadRequest("pin: {0}", bad);
            if (!announced) throw TiadException.BadRequest("{0}", "pin: announce it first (announced: true): a pin decides which CPU tiad trusts");
            DeviceIdentity previous = null;
            lock (pinsMu)
            {
                // An unreadable pin is replaced only with replace: true, and
                // the audit line says it was unreadable.
                bool unreadable = false;
                try
                {
                    previous = pins.Device(address);
                }
                catch (TiadException)
                {
                    if (!replace) throw;
                    unreadable = true;
                }
                if (previous != null && !replace)
                {
                    throw TiadException.BadRequest("pin: {0} is pinned already ({1}); replace it with replace: true", address, previous.Describe());
                }
                if (previous == null && !unreadable && replace) throw TiadException.NotFound("pin: {0} has no device pin to replace", address);
                pins.SetDevice(address, d, replace ? "re-pinned by hand" : "pinned by hand");
                Log.Line("audit pin {0} address={1} previous={2} device={3} holds={4}",
                    replace ? "replace" : "set", Json.Quote(address),
                    unreadable ? "unreadable" : previous == null ? "none" : Json.Write(previous.ToJson()), Json.Write(d.ToJson()), Holds(d));
            }
            Obj o = PinJson(address, d);
            if (previous != null) o["previous"] = previous.ToJson();
            return o;
        }

        // Unverifiable is what lifts a device_unverified refusal, and what
        // does not.
        static string Unverifiable(string address)
        {
            return "; it lifts only once tiad can read a CPU's identity online (#789): a re-pin (POST /v1/pins/" + address +
                ", audited) records a new identity but cannot lift this";
        }

        static TiadException Changed(string address, DeviceIdentity pinned, DeviceIdentity now, string diff, string what)
        {
            return new TiadException(409, Kinds.DeviceChanged,
                "device changed at " + address + ": " + diff + " (pinned: " + pinned.Describe() + "; now: " + now.Describe() + "). " + what +
                " If the CPU was replaced or updated on purpose, re-pin it: POST /v1/pins/" + address + " with replace: true (audited)");
        }

        // CheckDevicePin runs before an online call (compare, download, a
        // dry run too) to address: with a device pin, the CPU's identity
        // read online must match it; without one there is nothing to
        // check. An identity tiad cannot read fails closed.
        void CheckDevicePin(Session s, string address, string call)
        {
            DeviceIdentity pinned = pins.Device(address);
            if (pinned == null) return;
            DeviceIdentity now = OnSession(s, delegate(ITia t) { return t.Identify(s.Handle, address); });
            string bad = now == null ? "this agent's TIA side cannot read a CPU's identity online yet" : now.Validate();
            if (bad != null)
            {
                Log.Line("audit pin unverified call={0} session={1} address={2} why={3}", call, s.Id, Json.Quote(address), Json.Quote(bad));
                throw new TiadException(409, Kinds.DeviceUnverified,
                    address + " has a device pin, but its identity could not be read (" + bad + "), so the pin cannot be checked; the " + call + " was refused before it began" + Unverifiable(address));
            }
            string diff = DeviceIdentity.Mismatch(pinned, now);
            if (diff != null)
            {
                Log.Line("audit pin device-changed call={0} session={1} address={2} pinned={3} now={4}",
                    call, s.Id, Json.Quote(address), Json.Write(pinned.ToJson()), Json.Write(now.ToJson()));
                throw Changed(address, pinned, now, diff, "The " + call + " was refused before it began.");
            }
        }

        // PinUpload checks what an upload read against the device pin, or
        // pins it when there is none: trust on first use. It returns
        // "first-trust" or "matched".
        string PinUpload(Session s, string address, DeviceIdentity read, bool trust)
        {
            string bad = read == null ? "TIA's side read no identity" : read.Validate();
            lock (pinsMu)
            {
                DeviceIdentity pinned = pins.Device(address);
                if (bad != null)
                {
                    Log.Line("audit pin unverified call=upload session={0} address={1} why={2}", s.Id, Json.Quote(address), Json.Quote(bad));
                    throw new TiadException(409, Kinds.DeviceUnverified,
                        "the upload from " + address + " went through, but the CPU's identity could not be read from it (" + bad + "), so " +
                        (pinned == null ? "nothing was pinned" : "the device pin was not checked") + "; do not download from this session");
                }
                if (pinned == null)
                {
                    pins.SetDevice(address, read, "trusted on first use at an upload");
                    Log.Line("audit pin first-trust session={0} project={1} address={2} device={3} holds={4} trustUnverifiedCertificate={5}",
                        s.Id, Json.Quote(s.Project), Json.Quote(address), Json.Write(read.ToJson()), Holds(read), trust ? "yes" : "no");
                    return "first-trust";
                }
                string diff = DeviceIdentity.Mismatch(pinned, read);
                if (diff != null)
                {
                    Log.Line("audit pin device-changed call=upload session={0} address={1} pinned={2} now={3}",
                        s.Id, Json.Quote(address), Json.Write(pinned.ToJson()), Json.Write(read.ToJson()));
                    throw Changed(address, pinned, read, diff,
                        "The pin stands; the session's project now holds that CPU's program: do not download from it.");
                }
                List<string> extra = DeviceIdentity.Unpinned(pinned, read);
                if (extra.Count > 0)
                {
                    Log.Line("pin: the CPU at {0} reports {1}, which its pin does not hold; only a re-pin adds them",
                        Json.Quote(address), string.Join(", ", extra.ToArray()));
                }
                return "matched";
            }
        }
    }
}
