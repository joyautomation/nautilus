// Errors.cs: the failures tiad answers with (docs/design/siemens-ide.md section 3.2).
//
// C# 5 only: this source is compiled on the TIA machine by the stock
// .NET Framework csc, which knows nothing newer. No string interpolation,
// no ?., no nameof, no expression-bodied members.

using System;

namespace Tiad
{
    // Kinds are the error.kind values of the envelope. The Go client
    // (siemens/tiad) has the same list.
    public static class Kinds
    {
        public const string BadRequest = "bad_request";
        public const string Unauthorized = "unauthorized";
        public const string NotFound = "not_found";
        public const string Busy = "busy";
        public const string Gate = "gate";
        public const string TargetRefused = "target_refused";
        public const string DecisionRequired = "decision_required";
        public const string Declined = "declined";
        public const string CompileFailed = "compile_failed";
        public const string OperationFailed = "operation_failed";
    }

    // TiadException is a failure that becomes an error envelope: its HTTP
    // status, kind and message, and optionally data (the questions of a
    // refused download, the result of a failed compile).
    public sealed class TiadException : Exception
    {
        public readonly int Status;
        public readonly string Kind;
        public readonly bool Fatal;
        public readonly object Payload;

        public TiadException(int status, string kind, string message)
            : this(status, kind, message, null, false)
        {
        }

        public TiadException(int status, string kind, string message, object data, bool fatal)
            : base(message)
        {
            Status = status;
            Kind = kind;
            Payload = data;
            Fatal = fatal;
        }

        public static TiadException BadRequest(string format, params object[] args)
        {
            return new TiadException(400, Kinds.BadRequest, string.Format(format, args));
        }

        public static TiadException NotFound(string format, params object[] args)
        {
            return new TiadException(404, Kinds.NotFound, string.Format(format, args));
        }
    }

    // ConfigException is a configuration the agent refuses to start with.
    public sealed class ConfigException : Exception
    {
        public ConfigException(string message) : base(message)
        {
        }
    }
}
