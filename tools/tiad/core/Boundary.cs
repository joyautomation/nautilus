// Boundary.cs: what tiad throws back into TIA Portal (#789 stage 2).
//
// TIA Portal runs in its own process. Openness calls tiad's delegates from
// there (the download's pre- and after-download callbacks, the upload's
// callback, the online handler), and whatever a delegate throws goes back
// to TIA's process, which has to deserialize it. TIA has none of tiad's
// assemblies, so a tiad exception type kills TIA Portal: in stage 2 a
// Tiad.DownloadCancel thrown from the pre-download callback made TIA die
// with "Type 'Tiad.DownloadCancel' in Assembly 'tiad-core' is not marked as
// serializable". So a delegate TIA calls is never more than Boundary.Call,
// and the only exception that crosses is Fail's: a plain mscorlib
// OperationCanceledException carrying a message, with no inner exception
// and no Data (either could hold a type TIA cannot read).
//
// What a callback failed with is kept on tiad's side, as state, before
// anything is thrown: TIA may hand back a deserialized copy, wrapped in its
// own exception, or nothing at all, so no decision rests on getting the
// thrown object back.
//
// C# 5 only (see Errors.cs).

using System;

namespace Tiad
{
    public static class Boundary
    {
        // Fail is the exception tiad throws to TIA: why, and nothing else.
        public static OperationCanceledException Fail(string why)
        {
            return new OperationCanceledException(why);
        }

        // Call runs body, a delegate TIA calls back into; what names it for
        // the log. When body throws, keep runs first: it records the failure
        // in the caller's state and returns the reason to send TIA. Then the
        // reason is logged (with what body threw), and Fail(reason) is
        // thrown. A keep that throws still leaves a reason (body's own text);
        // a log that fails is dropped. Nothing but Fail's exception leaves.
        public static void Call(string what, Action body, Func<Exception, string> keep)
        {
            try
            {
                body();
            }
            catch (Exception e)
            {
                string why = null;
                try
                {
                    why = keep == null ? null : keep(e);
                }
                catch (Exception k)
                {
                    Quiet(what + ": recording a callback failure failed", k);
                }
                if (string.IsNullOrEmpty(why)) why = Text(e);
                try
                {
                    Log.Line("{0}: tiad cancels to TIA: {1} (thrown: {2})", what, TiaNames.LogSafe(why), TiaNames.LogSafe(Chain(e)));
                }
                catch (Exception)
                {
                }
                throw Fail(why);
            }
        }

        // Text is e's message with its inner ones, or its type when it has
        // none: never empty, so a reason always says something.
        static string Text(Exception e)
        {
            string m = "";
            try
            {
                m = TiaNames.Message(e);
            }
            catch (Exception)
            {
            }
            return string.IsNullOrEmpty(m) ? "tiad's callback failed (" + e.GetType().Name + ")" : m;
        }

        // Chain is e and its inner exceptions, type and message per level,
        // for the log: what came back from TIA, as it came back.
        public static string Chain(Exception e)
        {
            var b = new System.Text.StringBuilder();
            int n = 0;
            for (Exception i = e; i != null && n < 16; i = i.InnerException, n++)
            {
                if (n > 0) b.Append(" <- ");
                string m;
                try
                {
                    m = i.Message ?? "";
                }
                catch (Exception)
                {
                    m = "(message unreadable)";
                }
                b.Append(i.GetType().FullName).Append(": ").Append(m);
            }
            return b.ToString();
        }

        static void Quiet(string what, Exception e)
        {
            try
            {
                Log.Line("{0} ({1})", what, TiaNames.LogSafe(Chain(e)));
            }
            catch (Exception)
            {
            }
        }
    }
}
