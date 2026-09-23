// The SDK's event stream, captured per operation.
//
// docs/design/logix-sdk-api.md §3.8: a failed partial import says almost
// nothing through its exception and a great deal through its events. So
// logixd attaches a handler to every project and returns the events with
// every response — the caller gets the diagnosis, not just the verdict.
//
// OperationEvent is the SDK's abstract base (StdOutEventLogger extends the
// same three methods); deriving from it rather than implementing
// IOperationEvent keeps the LogStatus/LogError/LogProgress toggles the SDK
// already handles.

namespace Logixd;

using System.Collections.Concurrent;
using RockwellAutomation.LogixDesigner.Logging;

public sealed class CollectingLogger : OperationEvent
{
    private readonly ConcurrentQueue<LogEvent> _events = new();
    private const int Cap = 2000; // a runaway import must not exhaust memory

    public sealed record LogEvent(string Kind, string Source, string Message, int Percent);

    private void Push(LogEvent e)
    {
        while (_events.Count >= Cap) _events.TryDequeue(out _);
        _events.Enqueue(e);
    }

    /// <summary>Drains the events recorded since the last call.</summary>
    public List<LogEvent> Drain()
    {
        var outp = new List<LogEvent>();
        while (_events.TryDequeue(out var e)) outp.Add(e);
        return outp;
    }

    protected override void LogStatusMessage(string source, string message)
        => Push(new LogEvent("status", source ?? "", message ?? "", -1));

    protected override void LogErrorMessage(string source, string message)
        => Push(new LogEvent("error", source ?? "", message ?? "", -1));

    protected override void SetProgress(string source, int percent)
        => Push(new LogEvent("progress", source ?? "", "", percent));
}
