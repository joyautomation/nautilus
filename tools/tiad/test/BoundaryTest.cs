// BoundaryTest.cs: what tiad throws back into TIA (core/Boundary.cs, #789
// stage 2), checked against a stand-in for TIA's process: whatever a
// callback throws is serialized, then deserialized by a binder that knows
// mscorlib only (TIA Portal has none of tiad's assemblies), then handed
// back wrapped, as TIA may. A tiad type there is "TIA died".

using System;
using System.Collections.Generic;
using System.IO;
using System.Runtime.Serialization;
using System.Runtime.Serialization.Formatters.Binary;

namespace Tiad.Test
{
    static partial class Program
    {
        // OnlyMscorlib resolves the types TIA's process could: mscorlib's.
        sealed class OnlyMscorlib : SerializationBinder
        {
            public override Type BindToType(string assemblyName, string typeName)
            {
                if (assemblyName == typeof(object).Assembly.FullName) return typeof(object).Assembly.GetType(typeName, true);
                throw new SerializationException("Unable to find assembly '" + assemblyName + "' (TIA's process has only mscorlib here)");
            }
        }

        // NotSerializable is an exception no process can serialize.
        sealed class NotSerializable : Exception
        {
            public NotSerializable(string m) : base(m)
            {
            }
        }

        // Cross is what TIA's process gets back of e: a copy, or null when
        // TIA could not read it (TIA died, as in stage 2).
        static Exception Cross(Exception e, out string died)
        {
            died = null;
            try
            {
                using (var m = new MemoryStream())
                {
                    new BinaryFormatter().Serialize(m, e);
                    m.Position = 0;
                    var f = new BinaryFormatter();
                    f.Binder = new OnlyMscorlib();
                    return (Exception)f.Deserialize(m);
                }
            }
            catch (SerializationException x)
            {
                died = x.Message;
                return null;
            }
        }

        // ThroughTia calls callback as TIA does and returns what TIA's
        // download then throws back: the copy wrapped in TIA's own
        // exception (wrap) or as it is. null when the callback returned.
        static Exception ThroughTia(Action callback, bool wrap, string what)
        {
            try
            {
                callback();
                return null;
            }
            catch (Exception e)
            {
                string died;
                Exception copy = Cross(e, out died);
                if (copy == null)
                {
                    Fail(what + ": TIA died reading what tiad threw (" + e.GetType().FullName + "): " + died);
                    return new InvalidOperationException("TIA Portal has exited");
                }
                True(!object.ReferenceEquals(copy, e), what + ": TIA holds a copy");
                return wrap ? new InvalidOperationException("Exception in a download callback", copy) : copy;
            }
        }

        // Crossed checks one exception as TIA's process gets it.
        static void Crossed(Exception e, string message, string what)
        {
            if (e == null)
            {
                Fail(what + ": nothing thrown");
                return;
            }
            Eq(e.GetType(), typeof(OperationCanceledException), what + ": type");
            True(e.GetType().Assembly == typeof(object).Assembly, what + ": from mscorlib");
            True(e.InnerException == null, what + ": no inner exception");
            Eq(e.Data.Count, 0, what + ": no data");
            if (message != null) Eq(e.Message, message, what + ": message");
            string died;
            Exception copy = Cross(e, out died);
            True(copy != null, what + ": TIA can read it (" + died + ")");
            if (copy != null) Eq(copy.Message, e.Message, what + ": message after crossing");
        }

        static Exception Caught(Action f)
        {
            try
            {
                f();
            }
            catch (Exception e)
            {
                return e;
            }
            return null;
        }

        static void ATiadTypeKillsTia()
        {
            // The harness itself: what stage 2 threw does not cross.
            string died;
            True(Cross(new DownloadCancel("x", true), out died) == null, "DownloadCancel crosses");
            True(Cross(new TiaException("x"), out died) == null, "TiaException crosses");
            True(Cross(new InvalidOperationException("x", new TiaException("inner")), out died) == null, "a BCL exception holding a tiad one crosses");
            True(Cross(new OperationCanceledException("x"), out died) != null, "OperationCanceledException does not cross");
        }

        static void BoundarySendsOnlyACancel()
        {
            var bodies = new KeyValuePair<string, Exception>[]
            {
                new KeyValuePair<string, Exception>("DownloadCancel", new DownloadCancel("declined", true)),
                new KeyValuePair<string, Exception>("TiaException with an inner", new TiaException("outer", new TiadException(500, Kinds.OperationFailed, "inner"))),
                new KeyValuePair<string, Exception>("TiadException", new TiadException(409, Kinds.Declined, "no")),
                new KeyValuePair<string, Exception>("not serializable", new NotSerializable("odd")),
                new KeyValuePair<string, Exception>("NullReferenceException", new NullReferenceException()),
            };
            foreach (var b in bodies)
            {
                Exception thrown = b.Value;
                bool kept = false;
                Exception e = Caught(delegate
                {
                    Boundary.Call("test", delegate { throw thrown; }, delegate(Exception x)
                    {
                        kept = object.ReferenceEquals(x, thrown);
                        return "kept: " + x.Message;
                    });
                });
                True(kept, b.Key + ": keep saw what the body threw");
                Crossed(e, "kept: " + thrown.Message, b.Key);
            }
            // A keep that throws, or gives no reason: the body's own text.
            Crossed(Caught(delegate { Boundary.Call("test", delegate { throw new TiaException("body"); }, delegate(Exception x) { throw new NotSerializable("keep"); }); }), "body", "keep throws");
            Crossed(Caught(delegate { Boundary.Call("test", delegate { throw new TiaException("body"); }, delegate(Exception x) { return ""; }); }), "body", "keep says nothing");
            Crossed(Caught(delegate { Boundary.Call("test", delegate { throw new TiaException("body"); }, null); }), "body", "no keep");
            Crossed(Caught(delegate { Boundary.Call("test", delegate { throw new NotSerializable(""); }, null); }), null, "no text at all");
            // A body that returns throws nothing.
            True(Caught(delegate { Boundary.Call("test", delegate { }, delegate(Exception x) { throw new Exception("keep ran"); }); }) == null, "a body that returns");
        }

        // Pre is the download's pre-download callback as the glue writes it.
        static Action Pre(DownloadDecider d, object key, string type, Func<List<Pending>> list)
        {
            return delegate
            {
                Boundary.Call("download: before loading", delegate { d.Next(key, type, "msg", list); }, d.Keep);
            };
        }

        static void DeclineSurvivesTiasCopy()
        {
            foreach (string shape in new[] { "as thrown", "copy", "wrapped copy", "embedded" })
            {
                var r = new Run(delegate { return null; });
                object k = new object();
                Exception back;
                if (shape == "as thrown")
                {
                    back = Caught(Pre(r.D, k, "StopModules", delegate { return List(P(k, "StopModules", "stop")); }));
                }
                else if (shape == "embedded")
                {
                    Exception copy = ThroughTia(Pre(r.D, k, "StopModules", delegate { return List(P(k, "StopModules", "stop")); }), false, shape);
                    back = new InvalidOperationException("Download failed: " + copy.Message);
                }
                else
                {
                    back = ThroughTia(Pre(r.D, k, "StopModules", delegate { return List(P(k, "StopModules", "stop")); }), shape == "wrapped copy", shape);
                }
                if (shape == "as thrown") Crossed(back, r.D.Cancelled.Message, shape + ": what crossed");
                DownloadEnd end = r.D.Finish(true, null, "", back);
                True(end.Declined, shape + ": declined");
                True(end.Failure == null, shape + ": no failure (" + end.Failure + ")");
                True(!end.InDoubt, shape + ": not in doubt");
            }
        }

        static void DeclineNeedsItsOwnCancelBack()
        {
            // decide said no, but TIA threw something that is not that
            // cancel: in doubt, never declined.
            foreach (Exception other in new Exception[] { new InvalidOperationException("connection lost"), new OperationCanceledException(), new OperationCanceledException("the download was not") })
            {
                var r = new Run(delegate { return null; });
                object k = new object();
                Caught(Pre(r.D, k, "StopModules", delegate { return List(P(k, "StopModules", "stop")); }));
                True(r.D.Cancelled != null && r.D.Cancelled.Declined, other.Message + ": the decline is kept");
                DownloadEnd end = r.D.Finish(true, null, "", other);
                True(!end.Declined, Show(other.Message) + ": not declined");
                True(end.InDoubt, Show(other.Message) + ": in doubt");
            }
            // decide said no and TIA's after-download callback came: loaded.
            var l = new Run(delegate { return null; });
            object a = new object();
            Exception back = ThroughTia(Pre(l.D, a, "StopModules", delegate { return List(P(a, "StopModules", "stop")); }), true, "then loaded");
            l.D.After("StartModules");
            DownloadEnd e2 = l.D.Finish(true, null, "", back);
            True(!e2.Declined && e2.InDoubt, "a decline TIA loaded after is in doubt");
        }

        static void EveryCancelCrossesAndIsKept()
        {
            object k = new object();
            var cases = new KeyValuePair<string, Func<List<Pending>>>[]
            {
                new KeyValuePair<string, Func<List<Pending>>>("an unreadable list", delegate { return null; }),
                new KeyValuePair<string, Func<List<Pending>>>("a list that throws", delegate { throw new NotSerializable("parent disposed"); }),
                new KeyValuePair<string, Func<List<Pending>>>("a list without the callback", delegate { return List(P(new object(), "StopModules", "stop")); }),
                new KeyValuePair<string, Func<List<Pending>>>("an unknown configuration", delegate { return List(P(k, "ResetModule", "reset")); }),
            };
            foreach (var c in cases)
            {
                var r = new Run(AllYes);
                Exception back = ThroughTia(Pre(r.D, k, c.Key == "an unknown configuration" ? "ResetModule" : "StopModules", c.Value), true, c.Key);
                True(r.D.Cancelled != null && !r.D.Cancelled.Declined, c.Key + ": a failure kept");
                DownloadEnd end = r.D.Finish(true, null, "", back);
                Eq(end.Failure, r.D.Cancelled == null ? null : r.D.Cancelled.Message, c.Key + ": the kept reason");
                True(end.InDoubt && !end.Declined, c.Key + ": in doubt");
            }
            // decide itself throws: a failure, never a decline.
            var d = new Run(delegate(List<Question> qs) { throw new NotSerializable("decide broke"); });
            Exception b2 = ThroughTia(Pre(d.D, k, "StopModules", delegate { return List(P(k, "StopModules", "stop")); }), true, "decide throws");
            Contains(d.D.Cancelled == null ? null : d.D.Cancelled.Message, "decide broke", "decide throws: kept");
            DownloadEnd e3 = d.D.Finish(true, null, "", b2);
            True(e3.InDoubt && !e3.Declined, "decide throws: in doubt");
            // A failed write, before and after loading (Write's decider.Fail).
            foreach (bool after in new[] { false, true })
            {
                var w = Approved(k);
                Exception b3 = ThroughTia(delegate
                {
                    Boundary.Call("download", delegate
                    {
                        if (after) w.D.After("StartModules");
                        throw w.D.Fail("writing StopAll into StopModules failed: denied");
                    }, w.D.Keep);
                }, true, "a failed write");
                DownloadEnd e4 = w.D.Finish(true, null, "", b3);
                Eq(e4.Failure, "writing StopAll into StopModules failed: denied", (after ? "after" : "before") + ": the write's reason");
                True(e4.InDoubt, "a failed write: in doubt");
            }
        }

        static void ListThrowingCancelsNamingIt()
        {
            object k = new object();
            var r = new Run(AllYes);
            Caught(Pre(r.D, k, "StopModules", delegate { throw new NotSerializable("parent disposed"); }));
            Contains(r.D.Cancelled == null ? null : r.D.Cancelled.Message, "cannot list", "the reason");
            Contains(r.D.Cancelled == null ? null : r.D.Cancelled.Message, "parent disposed", "what failed");
            Eq(r.Decides, 0, "decide never ran");
        }

        static void DownloadStepsDeclineThroughTia()
        {
            // The whole route: TIA's download calls the callback, which
            // declines; TIA throws back a wrapped copy; DownloadSteps reads
            // a clean decline (nothing loaded), not outcome_unknown.
            var r = new Run(delegate { return null; });
            object k = new object();
            DownloadOutcome o = null;
            Exception e = Caught(delegate
            {
                o = DownloadSteps.Run(r.D, "192.0.2.1", delegate { }, delegate
                {
                    Exception back = ThroughTia(Pre(r.D, k, "StopModules", delegate { return List(P(k, "StopModules", "stop")); }), true, "steps");
                    throw back;
                }, delegate { }, delegate { return new List<string>(); });
            });
            True(e == null, "a decline throws nothing (" + (e == null ? "" : e.Message) + ")");
            True(o != null && !o.Loaded, "nothing loaded");
            // The same with a failure: in doubt.
            var f = new Run(AllYes);
            TiaException t = Throws<TiaException>(delegate
            {
                DownloadSteps.Run(f.D, "192.0.2.1", delegate { }, delegate
                {
                    throw ThroughTia(Pre(f.D, k, "StopModules", delegate { return null; }), true, "steps failure");
                }, delegate { }, delegate { return new List<string>(); });
            }, "a failure");
            if (t != null)
            {
                True(t.InDoubt, "a failure is in doubt");
                Contains(t.Message, "cannot list", "a failure names the kept reason");
            }
        }
    }
}
