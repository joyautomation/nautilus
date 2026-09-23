// Session management for logixd.
//
// The concurrency rules here are not ours; they come from the SDK, and
// docs/design/logix-sdk-api.md §6 quotes them:
//
//   - "Simultaneous projects opening is prohibited" — a Logix Designer
//     restriction, so every Open is serialized process-wide.
//   - Operations on DIFFERENT open projects may run concurrently.
//   - "an error in one project can cause all the projects to fail", so each
//     session is isolated and an exception never escapes its own request.
//
// Two more rules are ours, and both exist because the SDK will not enforce
// them:
//
//   - The SDK copies the project to its server side and leaves the client
//     file alone until Save, with NO file locking. Two agents saving the
//     same path silently clobber. logixd therefore refuses to open a path
//     another session already holds.
//   - A session left open holds a server-side project forever. Sessions
//     expire on an idle timer.

namespace Logixd;

using System.Collections.Concurrent;
using RockwellAutomation.LogixDesigner;

/// <summary>One open project, and the lock that serializes work on it.</summary>
public sealed class Session : IAsyncDisposable
{
    public string Id { get; }
    public string ProjectPath { get; }
    public LogixProject Project { get; }
    public CollectingLogger Log { get; }
    public DateTimeOffset OpenedAt { get; } = DateTimeOffset.UtcNow;
    public DateTimeOffset LastUsed { get; private set; } = DateTimeOffset.UtcNow;

    // One operation at a time per project: the SDK permits concurrency
    // ACROSS projects, never within one.
    private readonly SemaphoreSlim _gate = new(1, 1);

    public Session(string id, string projectPath, LogixProject project, CollectingLogger log)
        => (Id, ProjectPath, Project, Log) = (id, projectPath, project, log);

    /// <summary>Runs one operation against this project, serialized.</summary>
    public async Task<T> RunAsync<T>(Func<LogixProject, Task<T>> op, CancellationToken ct)
    {
        await _gate.WaitAsync(ct);
        try
        {
            LastUsed = DateTimeOffset.UtcNow;
            return await op(Project);
        }
        finally
        {
            LastUsed = DateTimeOffset.UtcNow;
            _gate.Release();
        }
    }

    public ValueTask DisposeAsync()
    {
        Project.Dispose();
        _gate.Dispose();
        return ValueTask.CompletedTask;
    }
}

/// <summary>
/// The session table, the process-wide open mutex, and the idle reaper.
/// </summary>
public sealed class SessionStore : IAsyncDisposable
{
    private readonly ConcurrentDictionary<string, Session> _sessions = new();
    private readonly SemaphoreSlim _openGate = new(1, 1);
    private readonly TimeSpan _idleTimeout;
    private readonly Timer _reaper;

    public SessionStore(TimeSpan idleTimeout)
    {
        _idleTimeout = idleTimeout;
        _reaper = new Timer(_ => ReapAsync().GetAwaiter().GetResult(), null,
            TimeSpan.FromSeconds(30), TimeSpan.FromSeconds(30));
    }

    public IReadOnlyCollection<Session> All => _sessions.Values.ToList();

    public Session? Get(string id) => _sessions.TryGetValue(id, out var s) ? s : null;

    /// <summary>
    /// Opens a project and registers a session. Opens are serialized
    /// process-wide; a path already held by another session is refused
    /// rather than opened twice.
    /// </summary>
    public async Task<Session> OpenAsync(string path, Func<CollectingLogger, Task<LogixProject>> open, CancellationToken ct)
    {
        var full = Path.GetFullPath(path);
        await _openGate.WaitAsync(ct);
        try
        {
            var held = _sessions.Values.FirstOrDefault(
                s => string.Equals(s.ProjectPath, full, StringComparison.OrdinalIgnoreCase));
            if (held is not null)
                throw new InvalidOperationException(
                    $"project is already open in session {held.Id}; the SDK does not lock project files, " +
                    "so two sessions on one path would silently overwrite each other");

            var log = new CollectingLogger();
            var project = await open(log);
            var session = new Session(Guid.NewGuid().ToString("N")[..12], full, project, log);
            _sessions[session.Id] = session;
            return session;
        }
        finally
        {
            _openGate.Release();
        }
    }

    public async Task<bool> CloseAsync(string id)
    {
        if (!_sessions.TryRemove(id, out var s)) return false;
        await s.DisposeAsync();
        return true;
    }

    private async Task ReapAsync()
    {
        var cutoff = DateTimeOffset.UtcNow - _idleTimeout;
        foreach (var s in _sessions.Values.Where(s => s.LastUsed < cutoff).ToList())
            await CloseAsync(s.Id);
    }

    public async ValueTask DisposeAsync()
    {
        await _reaper.DisposeAsync();
        foreach (var id in _sessions.Keys.ToList()) await CloseAsync(id);
        _openGate.Dispose();
    }
}
