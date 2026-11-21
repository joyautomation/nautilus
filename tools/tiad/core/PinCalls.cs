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

        // LogPins writes the pin inventory at startup, so a pin that
        // vanished between runs shows in the audit.
        void LogPins()
        {
            var l = new List<string>();
            foreach (string a in pins.Addresses())
            {
                PinRead r = pins.Read(a);
                l.Add(Json.Quote(a) + (r.State == PinState.Ok ? " holds=" + Holds(r.Device) : " " + r.State.ToString().ToLowerInvariant()));
            }
            Log.Line("audit pins at start dir={0} count={1} pins=[{2}]", Json.Quote(pins.Dir), l.Count, string.Join(", ", l.ToArray()));
        }

        void RegisterPins(Router r)
        {
            r.Add("GET", "/v1/pins", ListPins);
            r.Add("POST", "/v1/pins/{address}", SetPin);
            r.Add("POST", "/v1/identify", Identify);
            r.Add("POST", "/v1/sessions/{id}/online-check", OnlineCheck);
        }

        // OnlineCheck goes online read-only from a session's project to the
        // CPU at address and back offline (#789 S2b stage 1b). No pin is
        // set or checked, no CPU claimed, nothing saved. Announced, audited.
        object OnlineCheck(Call c)
        {
            NeedTia();
            return WithSession(c, delegate(Session s)
            {
                var r = c.Json();
                string address = r.Str("address");
                bool trust = r.Bool("trustUnverifiedCertificate");
                bool announced = r.Bool("announced");
                r.Done();
                if (!Ip.IsIPv4(address)) throw TiadException.BadRequest("online-check: address {0} is not an IPv4 address", Json.Quote(address));
                if (!Target(address)) throw new TiadException(403, Kinds.TargetRefused, address + " is not in TIAD_TARGETS");
                if (!announced) throw TiadException.BadRequest("{0}", "online-check: announce it first (announced: true): it reaches the CPU");
                OnlineCheckResult res = null;
                string outcome = "";
                try
                {
                    res = OnSession(s, delegate(ITia t) { return t.OnlineCheck(s.Handle, address, trust); });
                    outcome = "ok";
                }
                catch (TiadException e)
                {
                    outcome = e.Kind;
                    throw;
                }
                catch (Exception)
                {
                    outcome = "error";
                    throw;
                }
                finally
                {
                    Log.Line("audit online-check session={0} address={1} trustUnverifiedCertificate={2} state={3} asked={4} trustedBlindly={5} fingerprintItems={6} compared={7} differences={8} outcome={9}",
                        s.Id, Json.Quote(address), trust, res == null ? "-" : res.State, res == null ? 0 : res.Asked.Count,
                        res == null ? 0 : res.TrustedBlindly.Count, res == null ? 0 : res.Fingerprint.Count,
                        res != null && res.Compared, res == null ? 0 : res.Differences.Count, outcome);
                }
                var fp = new List<object>();
                foreach (KeyValuePair<string, string> kv in res.Fingerprint)
                {
                    var x = new Obj();
                    x["identifier"] = kv.Key;
                    x["value"] = kv.Value;
                    fp.Add(x);
                }
                var diffs = new List<object>();
                foreach (Difference d in res.Differences)
                {
                    var x = new Obj();
                    x["object"] = d.Object;
                    x["state"] = d.State;
                    diffs.Add(x);
                }
                var o = new Obj();
                o["projectCertificate"] = res.ProjectCertificate;
                o["state"] = res.State;
                o["onlineError"] = res.OnlineError;
                o["asked"] = res.Asked.ToArray();
                o["trustedBlindly"] = res.TrustedBlindly.ToArray();
                o["fingerprint"] = fp;
                o["fingerprintError"] = res.FingerprintError;
                o["compared"] = res.Compared;
                o["differences"] = diffs;
                o["compareError"] = res.CompareError;
                return o;
            });
        }

        // Identify reads, read-only, what TIA's fingerprint service reports
        // for the CPU at address (#789 S2b stage 1): no project, no pin set
        // or checked, no CPU claimed. It reaches the CPU, so it must be
        // announced, and it is audited with every value TIA returned (the
        // agent's log is the machine's own).
        object Identify(Call c)
        {
            NeedTia();
            var r = c.Json();
            string address = r.Str("address");
            bool trust = r.Bool("trustUnverifiedCertificate");
            bool announced = r.Bool("announced");
            r.Done();
            if (!Ip.IsIPv4(address)) throw TiadException.BadRequest("identify: address {0} is not an IPv4 address", Json.Quote(address));
            if (!Target(address)) throw new TiadException(403, Kinds.TargetRefused, address + " is not in TIAD_TARGETS");
            if (!announced) throw TiadException.BadRequest("{0}", "identify: announce it first (announced: true): it reaches the CPU");
            string outcome = "";
            FingerprintRead read = null;
            try
            {
                read = OnTia(delegate(ITia t) { return t.ReadFingerprint(address, trust); });
                outcome = "ok";
            }
            catch (TiadException e)
            {
                outcome = e.Kind;
                throw;
            }
            catch (Exception)
            {
                outcome = "error";
                throw;
            }
            finally
            {
                var items = new List<string>();
                if (read != null)
                {
                    foreach (KeyValuePair<string, string> kv in read.Items) items.Add(Json.Quote(kv.Key) + "=" + Json.Quote(kv.Value));
                }
                Log.Line("audit identify address={0} announced={1} trustUnverifiedCertificate={2} trustedBlindly={3} items=[{4}] outcome={5}",
                    Json.Quote(address), announced, trust, read == null ? 0 : read.TrustedBlindly.Count, string.Join(", ", items.ToArray()), outcome);
            }
            var list = new List<object>();
            foreach (KeyValuePair<string, string> kv in read.Items)
            {
                var x = new Obj();
                x["identifier"] = kv.Key;
                x["value"] = kv.Value;
                list.Add(x);
            }
            var o = new Obj();
            o["address"] = address;
            o["items"] = list;
            o["trustedBlindly"] = read.TrustedBlindly.ToArray();
            return o;
        }

        static Obj PinJson(string address, DeviceIdentity d)
        {
            var o = new Obj();
            o["address"] = address;
            o["device"] = d.ToJson();
            o["holds"] = d.Holds().ToArray();
            return o;
        }

        // Bounded is at most 200 characters of a corrupt pin's first line.
        static string Bounded(string s)
        {
            return s.Length <= 200 ? s : s.Substring(0, 200) + "...";
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
                // A pin whose content is corrupt is replaced only with
                // replace: true, and the audit line keeps why and what it
                // held. One that cannot be read at all (an I/O error, an
                // unreadable directory) is never replaced: its identity is
                // unknown, not wrong.
                PinRead read = pins.Read(address);
                if (read.State == PinState.Inaccessible || read.State == PinState.Corrupt && !replace)
                {
                    throw PinStore.Unreadable(address, read.Why);
                }
                bool unreadable = read.State == PinState.Corrupt;
                previous = read.Device;
                if (previous != null && !replace)
                {
                    throw TiadException.BadRequest("pin: {0} is pinned already ({1}); replace it with replace: true", address, previous.Describe());
                }
                if (previous == null && !unreadable && replace) throw TiadException.NotFound("pin: {0} has no device pin to replace", address);
                pins.SetDevice(address, d, replace ? "re-pinned by hand" : "pinned by hand");
                Log.Line("audit pin {0} address={1} previous={2} device={3} holds={4}",
                    replace ? "replace" : "set", Json.Quote(address),
                    unreadable ? "corrupt why=" + Json.Quote(read.Why) + " raw=" + Json.Quote(Bounded(read.Raw)) : previous == null ? "none" : Json.Write(previous.ToJson()),
                    Json.Write(d.ToJson()), Holds(d));
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
