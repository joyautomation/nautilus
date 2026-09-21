// logixd — the Logix Designer SDK, over HTTP, for nautilus.
//
// WHY THIS EXISTS. The SDK is a gRPC service bound to 127.0.0.1 only
// (docs/design/logix-sdk-api.md §2), reachable exclusively through a .NET or
// pythonnet client. There is no remote protocol. So anything that wants to
// drive a Logix project from elsewhere — the nautilus CLI on Linux, a CI
// job, a reviewer's laptop — needs an agent co-resident with the licensed
// Windows install. This is that agent.
//
// WHAT IT ADDS over calling the SDK directly, all of it from §6 and §7 of
// that document:
//
//   - Opens are serialized process-wide (the SDK forbids simultaneous
//     opens) while operations on different projects still run concurrently.
//   - One writer per project path, which the SDK does NOT enforce: it
//     copies the project server-side and leaves the file unlocked, so two
//     savers silently clobber.
//   - Every response carries the SDK's own event stream, because a failed
//     import says almost nothing through its exception.
//   - /v1/probe reports WHICH of the three independent licensing gates
//     failed. Each fails with a different uninformative message and
//     `flexsvr` reports absent and expired features identically; diagnosing
//     that by hand cost a full session once already.
//
// SECURITY POSTURE. Default bind is loopback. Binding anywhere else
// REQUIRES a token (LOGIXD_TOKEN) — a download stops a controller, and an
// unauthenticated endpoint that can do that on a plant network is a
// liability, not a convenience. Comm paths are additionally restricted to
// an operator-configured allowlist (LOGIXD_COMM_ALLOW), never to whatever
// the caller asks for.

using System.Diagnostics;
using System.Text.Json;
using System.Text.Json.Serialization;
using Logixd;
using RockwellAutomation.LogixDesigner;

// `logixd probe` runs the licensing check and exits, without binding a
// port. It exists to separate "the environment is wrong" from "the licence
// is missing" in ten seconds rather than an afternoon — the same probe
// code, run from a shell, against an agent that may be running as a
// service or in session 0.
//
// It has already earned that: a CreateNewProject timeout looked like a
// session-0 problem (FactoryTalk authentication plausibly wanting an
// interactive logon). Running this from an interactive session produced the
// identical timeout, which ruled the theory out and pointed at the real
// cause — no FactoryTalk activation on the machine at all.
if (args.Length > 0 && args[0] == "probe")
{
    uint? rev = args.Length > 1 && uint.TryParse(args[1], out var r) ? r : null;
    var probeWork = Environment.GetEnvironmentVariable("LOGIXD_WORKDIR") ?? @"C:\logixd-work";
    var (usable, payload) = await Probes.RunAsync(rev, probeWork, CancellationToken.None);
    Console.WriteLine(JsonSerializer.Serialize(payload, new JsonSerializerOptions
    {
        WriteIndented = true,
        PropertyNamingPolicy = JsonNamingPolicy.CamelCase,
    }));
    return usable ? 0 : 1;
}

// `logixd cnp` is a bisection harness, not a product verb: it calls
// CreateNewProjectAsync the way the SDK's own example does and lets each
// difference from logixd's own call be toggled, so "the example works and
// we do not" becomes one run instead of a morning.
if (args.Length >= 5 && args[0] == "cnp")
{
    var useCt = args.Contains("--ct");
    var mine = args.Contains("--mylogger");
    RockwellAutomation.LogixDesigner.Logging.OperationEvent lg =
        mine ? new CollectingLogger() : new RockwellAutomation.LogixDesigner.Logging.StdOutEventLogger();
    Console.WriteLine($"cnp: ct={useCt} logger={(mine ? "CollectingLogger" : "StdOutEventLogger")}");
    var sw = Stopwatch.StartNew();
    try
    {
        using var proj = useCt
            ? await LogixProject.CreateNewProjectAsync(args[1], uint.Parse(args[2]), args[3], args[4], lg, CancellationToken.None)
            : await LogixProject.CreateNewProjectAsync(args[1], uint.Parse(args[2]), args[3], args[4], lg);
        Console.WriteLine($"OK in {sw.Elapsed.TotalSeconds:F1}s");
        return 0;
    }
    catch (Exception ex)
    {
        Console.WriteLine($"FAIL in {sw.Elapsed.TotalSeconds:F1}s: {ex.GetType().Name}: {ex.Message}");
        return 1;
    }
}

var builder = WebApplication.CreateSlimBuilder(args);
builder.Services.ConfigureHttpJsonOptions(o =>
{
    o.SerializerOptions.PropertyNamingPolicy = JsonNamingPolicy.CamelCase;
    o.SerializerOptions.DefaultIgnoreCondition = JsonIgnoreCondition.WhenWritingNull;
});
builder.Logging.AddSimpleConsole(o => o.SingleLine = true);

var addr = Environment.GetEnvironmentVariable("LOGIXD_ADDR") ?? "http://127.0.0.1:8188";
var token = Environment.GetEnvironmentVariable("LOGIXD_TOKEN");
var commAllow = (Environment.GetEnvironmentVariable("LOGIXD_COMM_ALLOW") ?? "")
    .Split(',', StringSplitOptions.RemoveEmptyEntries | StringSplitOptions.TrimEntries)
    .ToArray();
var idleMinutes = int.TryParse(Environment.GetEnvironmentVariable("LOGIXD_IDLE_MINUTES"), out var im) ? im : 30;
// Every path this agent reads or writes on behalf of a caller lives under
// one root. A remote caller has to get an L5X TO the machine before it can
// be imported, and has to get an upload back, so file transfer is not
// optional — but an agent that will read or write any path it is told to is
// a file server with a controller attached.
var workDir = Path.GetFullPath(Environment.GetEnvironmentVariable("LOGIXD_WORKDIR") ?? @"C:\logixd-work");
Directory.CreateDirectory(workDir);

builder.WebHost.UseUrls(addr);
var app = builder.Build();

// A non-loopback bind without a token is refused at startup rather than
// served insecurely. Failing closed is the only safe default for a process
// that can stop a controller.
if (!Loopback(addr) && string.IsNullOrEmpty(token))
{
    Console.Error.WriteLine(
        $"logixd: refusing to bind {addr} without LOGIXD_TOKEN. This agent can download to a " +
        "controller; an unauthenticated one on a routable address is a hazard. Set LOGIXD_TOKEN, " +
        "or bind loopback and tunnel.");
    return 2;
}

await using var sessions = new SessionStore(TimeSpan.FromMinutes(idleMinutes));

// ---------------------------------------------------------------- plumbing

app.Use(async (ctx, next) =>
{
    if (!string.IsNullOrEmpty(token))
    {
        var got = ctx.Request.Headers.Authorization.ToString();
        if (got != "Bearer " + token)
        {
            ctx.Response.StatusCode = StatusCodes.Status401Unauthorized;
            await ctx.Response.WriteAsJsonAsync(new { error = "unauthorized" });
            return;
        }
    }
    await next();
});

static bool Loopback(string url) =>
    url.Contains("127.0.0.1") || url.Contains("localhost") || url.Contains("[::1]");

// Ok wraps a result with the SDK events that produced it.
static IResult Ok(object? data, CollectingLogger? log) =>
    Results.Json(new { ok = true, data, events = log?.Drain() });

// Fail classifies the SDK's exception hierarchy for the caller.
// §7: OperationFailed means the parameters or the controller's state were
// wrong and the caller can fix it; OperationNotPerformed means the SDK or
// the channel broke and the session should be torn down. Collapsing the two
// would hide exactly the distinction a caller needs.
static IResult Fail(Exception ex, CollectingLogger? log)
{
    var (status, kind, fatal) = ex switch
    {
        OperationFailedException => (StatusCodes.Status409Conflict, "operation_failed", false),
        OperationNotPerformedException => (StatusCodes.Status502BadGateway, "operation_not_performed", true),
        LogixSdkException => (StatusCodes.Status500InternalServerError, "sdk", true),
        InvalidOperationException => (StatusCodes.Status409Conflict, "conflict", false),
        FileNotFoundException => (StatusCodes.Status404NotFound, "not_found", false),
        ArgumentException => (StatusCodes.Status400BadRequest, "bad_request", false),
        _ => (StatusCodes.Status500InternalServerError, "internal", true),
    };
    return Results.Json(new
    {
        ok = false,
        error = new { kind, message = ex.Message, type = ex.GetType().Name, fatal },
        events = log?.Drain(),
    }, statusCode: status);
}

// Guard runs an operation against a named session.
async Task<IResult> WithSession(string? id, Func<Session, Task<object?>> op, CancellationToken ct)
{
    if (string.IsNullOrWhiteSpace(id)) return Results.BadRequest(new { ok = false, error = new { kind = "bad_request", message = "session is required" } });
    var s = sessions.Get(id!);
    if (s is null) return Results.NotFound(new { ok = false, error = new { kind = "not_found", message = $"no session {id}" } });
    try { return Ok(await s.RunAsync(_ => op(s), ct), s.Log); }
    catch (Exception ex) { return Fail(ex, s.Log); }
}

// ------------------------------------------------------------- file access
//
// Scoped to workDir, always. Resolve() is the whole security boundary: it
// rejects absolute paths, traversal, and symlink escapes by comparing the
// FULLY RESOLVED path against the root, not the string the caller sent.

string Resolve(string rel)
{
    if (string.IsNullOrWhiteSpace(rel)) throw new ArgumentException("path is required");
    if (Path.IsPathRooted(rel)) throw new ArgumentException("path must be relative to the agent's work directory");
    var full = Path.GetFullPath(Path.Combine(workDir, rel));
    var root = workDir.TrimEnd(Path.DirectorySeparatorChar) + Path.DirectorySeparatorChar;
    if (!full.StartsWith(root, StringComparison.OrdinalIgnoreCase))
        throw new ArgumentException("path escapes the agent's work directory");
    return full;
}

app.MapGet("/v1/workdir", () => Ok(new { workDir }, null));

app.MapPut("/v1/files/{**path}", async (string path, HttpRequest req) =>
{
    try
    {
        var full = Resolve(path);
        Directory.CreateDirectory(Path.GetDirectoryName(full)!);
        await using (var fs = File.Create(full)) await req.Body.CopyToAsync(fs);
        return Ok(new { path, bytes = new FileInfo(full).Length }, null);
    }
    catch (Exception ex) { return Fail(ex, null); }
});

app.MapGet("/v1/files/{**path}", (string path) =>
{
    try
    {
        var full = Resolve(path);
        if (!File.Exists(full)) return Fail(new FileNotFoundException($"no such file: {path}"), null);
        return Results.File(full, "application/octet-stream");
    }
    catch (Exception ex) { return Fail(ex, null); }
});

app.MapDelete("/v1/files/{**path}", (string path) =>
{
    try
    {
        var full = Resolve(path);
        if (File.Exists(full)) File.Delete(full);
        return Ok(new { deleted = path }, null);
    }
    catch (Exception ex) { return Fail(ex, null); }
});

app.MapGet("/v1/files", () =>
{
    var root = new DirectoryInfo(workDir);
    var files = root.EnumerateFiles("*", SearchOption.AllDirectories)
        .Select(f => new { path = Path.GetRelativePath(workDir, f.FullName).Replace('\\', '/'), bytes = f.Length, modified = f.LastWriteTimeUtc })
        .OrderBy(f => f.path).ToList();
    return Ok(new { workDir, files }, null);
});

// ------------------------------------------------------------------ health

app.MapGet("/v1/health", () => Results.Json(new
{
    ok = true,
    data = new
    {
        service = "logixd",
        version = typeof(Program).Assembly.GetName().Version?.ToString(),
        sdkClient = typeof(LogixProject).Assembly.GetName().Version?.ToString(),
        sessions = sessions.All.Count,
        commAllowlist = commAllow,
    },
}));

// ------------------------------------------------------------------- probe
//
// The three gates, named individually. This is the product requirement from
// logix-target.md §14: FTSP auth, the FlexNet LDSDK.EXE feature and a
// CodeMeter entitlement each fail differently and uninformatively, and on
// one machine licensing fails first while on another FTSP does — so you
// never see the second problem until you fix the first.
//
// The definitive gate is the live one: create a throwaway project. If that
// succeeds, all three passed, whatever the individual probes say.

// Always 200. A probe that reports "the SDK is unusable" has SUCCEEDED —
// that is the answer the caller asked for, and burying it in a 5xx makes
// every client treat a correct diagnosis as a transport failure. The
// verdict is data.usable; monitoring reads the field.
app.MapGet("/v1/probe", async (uint? revision, CancellationToken ct) =>
{
    var (_, payload) = await Probes.RunAsync(revision, workDir, ct);
    return Results.Json(new { ok = true, data = payload });
});

// ---------------------------------------------------------------- sessions

app.MapPost("/v1/sessions", async (OpenReq req, CancellationToken ct) =>
{
    try
    {
        var s = await sessions.OpenAsync(req.Project,
            log => LogixProject.OpenLogixProjectAsync(req.Project, log, ct), ct);
        return Ok(new { session = s.Id, project = s.ProjectPath, openedAt = s.OpenedAt }, s.Log);
    }
    catch (Exception ex) { return Fail(ex, null); }
});

app.MapGet("/v1/sessions", () => Ok(sessions.All
    .Select(s => new { session = s.Id, project = s.ProjectPath, openedAt = s.OpenedAt, lastUsed = s.LastUsed })
    .ToList(), null));

app.MapDelete("/v1/sessions/{id}", async (string id) =>
    await sessions.CloseAsync(id)
        ? Ok(new { closed = id }, null)
        : Results.NotFound(new { ok = false, error = new { kind = "not_found", message = $"no session {id}" } }));

// ------------------------------------------------- whole-project lifecycle

// convert is the ACD <-> L5X round trip, and it is two SDK calls: Open
// accepts ACD/L5K/L5X and SaveAs writes whichever the extension names
// (§3.1). detailedL5x controls the L5X ExportOptions — References, Context,
// ProductDefinedTypes and IOTags — so `false` is the lean export that
// diffs best.
app.MapPost("/v1/convert", async (ConvertReq req, CancellationToken ct) =>
{
    var log = new CollectingLogger();
    try
    {
        var s = await sessions.OpenAsync(req.Input,
            l => LogixProject.OpenLogixProjectAsync(req.Input, l, ct), ct);
        try
        {
            await s.RunAsync(async p =>
            {
                await p.SaveAsAsync(req.Output, req.Force, req.DetailedL5x, ct);
                return 0;
            }, ct);
            var len = new FileInfo(req.Output).Length;
            return Ok(new { input = req.Input, output = req.Output, bytes = len, detailedL5x = req.DetailedL5x }, s.Log);
        }
        finally { await sessions.CloseAsync(s.Id); }
    }
    catch (Exception ex) { return Fail(ex, log); }
});

app.MapPost("/v1/create", async (CreateReq req, CancellationToken ct) =>
{
    var log = new CollectingLogger();
    try
    {
        using var p = await LogixProject.CreateNewProjectAsync(
            req.Project, req.MajorRevision, req.ProcessorType, req.ControllerName, log, ct);
        return Ok(new { project = req.Project, bytes = new FileInfo(req.Project).Length }, log);
    }
    catch (Exception ex) { return Fail(ex, log); }
});

app.MapPost("/v1/sessions/{id}/save", async (string id, SaveReq? req, CancellationToken ct) =>
    await WithSession(id, async s =>
    {
        if (string.IsNullOrEmpty(req?.Path)) { await s.Project.SaveAsync(ct); return new { saved = s.ProjectPath }; }
        await s.Project.SaveAsAsync(req!.Path!, req.Force, req.DetailedL5x, ct);
        return new { saved = req.Path! };
    }, ct));

// build compiles the user subroutines and caches the binaries in the .ACD,
// so a later download does not recompile. v37+. The target must match the
// controller the project will be downloaded to (emulated vs physical) or
// the compile happens twice.
app.MapPost("/v1/sessions/{id}/build", async (string id, BuildReq? req, CancellationToken ct) =>
    await WithSession(id, async s =>
    {
        var target = ParseEnum(req?.Target, LogixProject.RequestedBuildTarget.DefaultTarget);
        var sw = Stopwatch.StartNew();
        await s.Project.BuildAsync(target, ct);
        return new { target = target.ToString(), elapsedMs = sw.ElapsedMilliseconds };
    }, ct));

app.MapGet("/v1/sessions/{id}/executables", async (string id, CancellationToken ct) =>
    await WithSession(id, async s => new { executables = await s.Project.GetAllExecutablesAsync(ct) }, ct));

// --------------------------------------------------- partial import/export

app.MapPost("/v1/sessions/{id}/partial-export", async (string id, PartialExportReq req, CancellationToken ct) =>
    await WithSession(id, async s =>
    {
        // The SDK refuses to overwrite; deleting first makes the endpoint
        // idempotent, which is what a re-run of a CI job needs.
        if (req.Force && File.Exists(req.Output)) File.Delete(req.Output);
        await s.Project.PartialExportToXmlFileAsync(req.XPath, req.Output, ct);
        return new { xpath = req.XPath, output = req.Output, bytes = new FileInfo(req.Output).Length };
    }, ct));

// Offline-only. Honours per-node Use="Delete|Create|Update|Overwrite|Ignore",
// so a hand-built L5X is a scripted edit.
app.MapPost("/v1/sessions/{id}/partial-import", async (string id, PartialImportReq req, CancellationToken ct) =>
    await WithSession(id, async s =>
    {
        var collision = ParseEnum(req.Collision, LogixProject.ImportCollisionOptions.OverwriteOnColl);
        await s.Project.PartialImportFromXmlFileAsync(req.XPath, req.File, collision, req.ContinueOnErrors, ct);
        return new { xpath = req.XPath, file = req.File, collision = collision.ToString() };
    }, ct));

// ONLINE-CAPABLE. This and import-rungs are the two calls that can change a
// running controller — the finding that corrected logix-sdk-api.md §9.
// onlineOption is ignored offline.
app.MapPost("/v1/sessions/{id}/partial-import-with-target", async (string id, PartialImportTargetReq req, CancellationToken ct) =>
    await WithSession(id, async s =>
    {
        var opt = ParseEnum(req.OnlineOption, LogixProject.PartialImportOption.LeaveEdits);
        await s.Project.PartialImportWithTargetFromXmlFileAsync(req.XPath, req.TargetName, req.File, opt, ct);
        return new { xpath = req.XPath, targetName = req.TargetName, file = req.File, onlineOption = opt.ToString() };
    }, ct));

// ONLINE-CAPABLE, and the interesting one: a rung range, at a position,
// replacing n existing rungs. onlineOption is the test / accept / assemble
// workflow — LeaveEdits leaves pending edits, AcceptEdits sends them down,
// FinalizeEdits additionally assembles if the controller is in Run.
app.MapPost("/v1/sessions/{id}/import-rungs", async (string id, ImportRungsReq req, CancellationToken ct) =>
    await WithSession(id, async s =>
    {
        var opt = ParseEnum(req.OnlineOption, LogixProject.PartialImportOption.LeaveEdits);
        var sw = Stopwatch.StartNew();
        await s.Project.PartialImportRungsFromXmlFileAsync(
            req.XPath, req.InsertPosition, req.ReplaceCount, req.File, opt, ct);
        return new
        {
            xpath = req.XPath,
            insertPosition = req.InsertPosition,
            replaceCount = req.ReplaceCount,
            onlineOption = opt.ToString(),
            elapsedMs = sw.ElapsedMilliseconds,
        };
    }, ct));

// ------------------------------------------------------ controller control

// The comm path is checked against an operator-configured allowlist held
// HERE, never supplied by the caller: a pipeline must not be able to name a
// controller nobody blessed. logix-target.md §15.2, gate 2.
app.MapPost("/v1/sessions/{id}/comm-path", async (string id, CommPathReq req, CancellationToken ct) =>
{
    if (commAllow.Length > 0 && !commAllow.Contains(req.Path, StringComparer.OrdinalIgnoreCase))
        return Results.Json(new
        {
            ok = false,
            error = new
            {
                kind = "forbidden",
                message = $"comm path '{req.Path}' is not in this agent's allowlist ({string.Join(", ", commAllow)}). " +
                          "The allowlist is agent-side config, not a request parameter.",
                fatal = false,
            },
        }, statusCode: StatusCodes.Status403Forbidden);
    return await WithSession(id, async s =>
    {
        await s.Project.SetCommunicationsPathAsync(req.Path, ct);
        return new { commPath = await s.Project.GetCommunicationsPathAsync(ct) };
    }, ct);
});

app.MapGet("/v1/sessions/{id}/state", async (string id, CancellationToken ct) =>
    await WithSession(id, async s => new
    {
        connected = (await s.Project.ReadConnectedStateAsync(ct)).ToString(),
        commPath = await s.Project.GetCommunicationsPathAsync(ct),
    }, ct));

app.MapPost("/v1/sessions/{id}/online", async (string id, CancellationToken ct) =>
    await WithSession(id, async s =>
    {
        await s.Project.GoOnlineAsync(ct);
        return new { connected = (await s.Project.ReadConnectedStateAsync(ct)).ToString() };
    }, ct));

app.MapPost("/v1/sessions/{id}/offline", async (string id, CancellationToken ct) =>
    await WithSession(id, async s =>
    {
        await s.Project.GoOfflineAsync(ct);
        return new { connected = (await s.Project.ReadConnectedStateAsync(ct)).ToString() };
    }, ct));

app.MapGet("/v1/sessions/{id}/mode", async (string id, CancellationToken ct) =>
    await WithSession(id, async s => new { mode = (await s.Project.ReadControllerModeAsync(ct)).ToString() }, ct));

app.MapPost("/v1/sessions/{id}/mode", async (string id, ModeReq req, CancellationToken ct) =>
    await WithSession(id, async s =>
    {
        await s.Project.ChangeControllerModeAsync(
            ParseEnum(req.Mode, LogixProject.RequestedControllerMode.Program), ct);
        return new { mode = (await s.Project.ReadControllerModeAsync(ct)).ToString() };
    }, ct));

// A download STOPS the controller and resets tags to project values, and —
// unlike the GUI — the SDK neither changes the mode for you nor checks that
// it is right (§3.4). The mode transition is therefore explicit here, and
// only when the caller asks for it, so nobody stops a line by omission.
app.MapPost("/v1/sessions/{id}/download", async (string id, DownloadReq? req, CancellationToken ct) =>
    await WithSession(id, async s =>
    {
        var sw = Stopwatch.StartNew();
        if (req?.EnsureProgramMode == true)
        {
            await s.Project.ChangeControllerModeAsync(LogixProject.RequestedControllerMode.Program, ct);
            await s.Project.GoOfflineAsync(ct);
        }
        await s.Project.DownloadAsync(ct);
        return new { downloaded = s.ProjectPath, elapsedMs = sw.ElapsedMilliseconds };
    }, ct));

app.MapPost("/v1/upload-to-new", async (UploadToNewReq req, CancellationToken ct) =>
{
    var log = new CollectingLogger();
    try
    {
        if (commAllow.Length > 0 && !commAllow.Contains(req.CommPath, StringComparer.OrdinalIgnoreCase))
            return Results.Json(new { ok = false, error = new { kind = "forbidden", message = $"comm path '{req.CommPath}' is not in this agent's allowlist" } },
                statusCode: StatusCodes.Status403Forbidden);
        using var p = await LogixProject.UploadToNewProjectAsync(req.CommPath, req.Output, log, ct);
        return Ok(new { output = req.Output, bytes = new FileInfo(req.Output).Length }, log);
    }
    catch (Exception ex) { return Fail(ex, log); }
});

// -------------------------------------------------------------- tag values
//
// Offline reads/writes the value in the project file; online goes to the
// controller and costs ~0.5 s per tag (the SDK's own documentation). Never
// route anything at scan rate through here — that is what eip/ is for.

app.MapPost("/v1/sessions/{id}/tag/get", async (string id, TagGetReq req, CancellationToken ct) =>
    await WithSession(id, async s =>
    {
        var mode = ParseEnum(req.Mode, LogixProject.OperationMode.Offline);
        object? value = req.Type?.ToUpperInvariant() switch
        {
            "BOOL" => await s.Project.GetTagValueBOOLAsync(req.TagPath, mode),
            "SINT" => await s.Project.GetTagValueSINTAsync(req.TagPath, mode),
            "INT" => await s.Project.GetTagValueINTAsync(req.TagPath, mode),
            "DINT" => await s.Project.GetTagValueDINTAsync(req.TagPath, mode),
            "LINT" => await s.Project.GetTagValueLINTAsync(req.TagPath, mode),
            "USINT" => await s.Project.GetTagValueUSINTAsync(req.TagPath, mode),
            "UINT" => await s.Project.GetTagValueUINTAsync(req.TagPath, mode),
            "UDINT" => await s.Project.GetTagValueUDINTAsync(req.TagPath, mode),
            "ULINT" => await s.Project.GetTagValueULINTAsync(req.TagPath, mode),
            "REAL" => await s.Project.GetTagValueREALAsync(req.TagPath, mode),
            "LREAL" => await s.Project.GetTagValueLREALAsync(req.TagPath, mode),
            "STRING" => await s.Project.GetTagValueSTRINGAsync(req.TagPath, mode),
            _ => throw new ArgumentException($"unsupported tag type '{req.Type}'"),
        };
        return new { tagPath = req.TagPath, type = req.Type, mode = mode.ToString(), value };
    }, ct));

app.MapPost("/v1/sessions/{id}/tag/set", async (string id, TagSetReq req, CancellationToken ct) =>
    await WithSession(id, async s =>
    {
        var mode = ParseEnum(req.Mode, LogixProject.OperationMode.Offline);
        var v = req.Value;
        switch (req.Type?.ToUpperInvariant())
        {
            case "BOOL": await s.Project.SetTagValueBOOLAsync(req.TagPath, mode, v.GetBoolean()); break;
            case "SINT": await s.Project.SetTagValueSINTAsync(req.TagPath, mode, v.GetSByte()); break;
            case "INT": await s.Project.SetTagValueINTAsync(req.TagPath, mode, v.GetInt16()); break;
            case "DINT": await s.Project.SetTagValueDINTAsync(req.TagPath, mode, v.GetInt32()); break;
            case "LINT": await s.Project.SetTagValueLINTAsync(req.TagPath, mode, v.GetInt64()); break;
            case "USINT": await s.Project.SetTagValueUSINTAsync(req.TagPath, mode, v.GetByte()); break;
            case "UINT": await s.Project.SetTagValueUINTAsync(req.TagPath, mode, v.GetUInt16()); break;
            case "UDINT": await s.Project.SetTagValueUDINTAsync(req.TagPath, mode, v.GetUInt32()); break;
            case "ULINT": await s.Project.SetTagValueULINTAsync(req.TagPath, mode, v.GetUInt64()); break;
            case "REAL": await s.Project.SetTagValueREALAsync(req.TagPath, mode, v.GetSingle()); break;
            case "LREAL": await s.Project.SetTagValueLREALAsync(req.TagPath, mode, v.GetDouble()); break;
            case "STRING": await s.Project.SetTagValueSTRINGAsync(req.TagPath, mode, v.GetString() ?? ""); break;
            default: throw new ArgumentException($"unsupported tag type '{req.Type}'");
        }
        return new { tagPath = req.TagPath, type = req.Type, mode = mode.ToString() };
    }, ct));

app.Run();
return 0;

// ParseEnum maps a request string onto an SDK enum, case-insensitively,
// naming the legal values when it cannot — an unrecognised mode should say
// what the modes are, not just "invalid".
static TEnum ParseEnum<TEnum>(string? s, TEnum fallback) where TEnum : struct, Enum
{
    if (string.IsNullOrWhiteSpace(s)) return fallback;
    if (Enum.TryParse<TEnum>(s, ignoreCase: true, out var v)) return v;
    throw new ArgumentException(
        $"unknown {typeof(TEnum).Name} '{s}'; expected one of: {string.Join(", ", Enum.GetNames<TEnum>())}");
}

// ------------------------------------------------------------- request DTOs

record OpenReq(string Project);
record CreateReq(string Project, uint MajorRevision, string ProcessorType, string ControllerName);
record ConvertReq(string Input, string Output, bool Force = true, bool DetailedL5x = false);
record SaveReq(string? Path, bool Force = true, bool DetailedL5x = false);
record BuildReq(string? Target);
record PartialExportReq(string XPath, string Output, bool Force = true);
record PartialImportReq(string XPath, string File, string? Collision, bool ContinueOnErrors = false);
record PartialImportTargetReq(string XPath, string TargetName, string File, string? OnlineOption);
record ImportRungsReq(string XPath, uint InsertPosition, uint ReplaceCount, string File, string? OnlineOption);
record CommPathReq(string Path);
record ModeReq(string Mode);
record DownloadReq(bool EnsureProgramMode = false);
record UploadToNewReq(string CommPath, string Output);
record TagGetReq(string TagPath, string Type, string? Mode);
record TagSetReq(string TagPath, string Type, string? Mode, JsonElement Value);


// Probes is the licensing check, shared by `logixd probe` and GET /v1/probe.
static class Probes
{
    public static async Task<(bool usable, object payload)> RunAsync(uint? revision, string workDir, CancellationToken ct)
    {
    var gates = new List<object>();

    gates.Add(await Probe("sdk-service", () =>
    {
        var svc = Process.GetProcessesByName("LdSdkServer");
        return Task.FromResult<(bool, string)>((svc.Length > 0,
            svc.Length > 0 ? "LdSdkServer running" : "LdSdkServer is not running"));
    }));

    // WHICH versions are installed decides what a project may be created
    // as, and asking for one that is not installed does not fail cleanly —
    // it times out. So the version is discovered, never assumed.
    var installed = InstalledLogixVersions();
    gates.Add(new
    {
        name = "logix-designer",
        ok = installed.Length > 0,
        detail = installed.Length > 0
            ? "installed: " + string.Join(", ", installed.Select(v => "v" + v))
            : "no Logix Designer found under Studio 5000\\Logix Designer\\ENU",
    });

    // The live gate. It must exercise the whole stack — FTSP auth, the gRPC
    // channel, the Logix services for that revision — WITHOUT depending on
    // any string the prober guessed.
    //
    // GetProcessorTypes is that call. The first version of this probe used
    // CreateNewProject with a hard-coded "1756-L85E", and when a parameter
    // is wrong the SDK does not say so: it HANGS until its own timeout, and
    // a TimeoutException reads exactly like a licensing failure. That cost
    // an afternoon and produced a wrong conclusion ("this machine has no
    // activation") while Open and SaveAs were working the whole time.
    //
    // So: ask the SDK what it supports, then — only if it answered — create
    // a project using a name it gave us.
    var rev = revision ?? (installed.Length > 0 ? installed[^1] : 0u);
    string? typesDetail;
    var typesOk = false;
    string? firstType = null;
    var typeCount = 0;
    try
    {
        if (rev == 0) throw new InvalidOperationException("no installed Logix Designer version to test with");
        var types = await LogixProject.GetProcessorTypesAsync(rev, ct);
        foreach (var kv in types)
        {
            typeCount++;
            firstType ??= kv.Key;
        }
        typesOk = typeCount > 0;
        typesDetail = typesOk
            ? $"v{rev} offers {typeCount} processor types, e.g. {firstType}"
            : $"v{rev} returned no processor types";
    }
    catch (Exception ex)
    {
        typesDetail = Describe(ex);
    }
    gates.Add(new { name = "live-sdk-call", ok = typesOk, detail = typesDetail });

    string? liveDetail = null;
    var liveOk = false;
    if (typesOk)
    {
        // NOT Path.GetTempPath(). LdSdkServer runs as a Windows service and
        // creates the project file ITSELF, server-side — so a path under the
        // calling user's profile is one the service cannot write. It does not
        // say so: the call hangs until its own timeout and surfaces as a
        // TimeoutException inside the FactoryTalk login, which reads exactly
        // like an authentication failure. Cost: most of a morning, and a
        // wrong conclusion about the machine's licensing.
        Directory.CreateDirectory(workDir);
        var tmp = Path.Combine(workDir, $"logixd-probe-{Guid.NewGuid():N}.ACD");
        try
        {
            var log = new CollectingLogger();
            using (var p = await LogixProject.CreateNewProjectAsync(tmp, rev, firstType!, "LogixdProbe", log, ct)) { }
            liveOk = true;
            liveDetail = $"created and closed a v{rev} {firstType} project";
        }
        catch (Exception ex)
        {
            liveDetail = Describe(ex);
        }
        finally
        {
            try { if (File.Exists(tmp)) File.Delete(tmp); } catch { /* best effort */ }
        }
    }
    else
    {
        liveDetail = "skipped — the SDK did not answer the query above";
    }
    // Informational, and deliberately NOT part of the verdict. On the
    // reference host CreateNewProject fails intermittently — including in
    // the SDK's own shipped example, on a freshly restarted service, while
    // OpenLogixProject + SaveAs succeed either side of it. Gating "is the
    // SDK usable" on the flakiest call in the API would make the probe lie
    // about a machine that can do real work.
    gates.Add(new { name = "create-project", ok = liveOk, informational = true, detail = liveDetail });

    return (typesOk, (object)new
    {
            usable = typesOk,
            revisionTested = rev,
            installedRevisions = installed,
            gates,
            hint = typesOk ? null :
                "Read the live-* details above before assuming a licence problem. MEASURED on this " +
                "codebase's reference host: the SDK requests the FlexNet feature LDSDK.EXE, is refused " +
                "(\"No such feature exists\"), and opens and saves projects anyway — so a denial in " +
                "RSsvr.log does NOT by itself explain a failure. " +
                "A GetTokenForUserAsync timeout means FactoryTalk authentication, which is " +
                "CONFIGURATION, not licensing: check that HKLM\\SOFTWARE\\WOW6432Node\\Rockwell " +
                "Software\\FactoryTalk has a Directories key, and if it does not, configure the " +
                "FactoryTalk Local Directory with FTDConfigurationUtility.exe (GUI only — it needs a " +
                "console or RDP session). " +
                "Only if live-sdk-call ALSO fails is licensing the likely cause: FTACmdUtility " +
                "listAvailable, RSsvr.log for the feature name, cmu --list-content for CodeMeter. " +
                "flexsvr reports absent and expired features identically.",
    });


    }

    // Describe unwraps the exception chain. A bare "The operation has timed
    // out." is the least useful thing the SDK can say, and the inner frames
    // are where the cause lives — an FTSP token timeout names
    // GetTokenForUserAsync, which is a configuration problem, not a licence.
    static string Describe(Exception ex)
    {
        var parts = new List<string>();
        for (var e = ex; e is not null; e = e.InnerException)
            parts.Add($"{e.GetType().Name}: {e.Message}");
        var where = ex.StackTrace?
            .Split('\n')
            .Select(l => l.Trim())
            .FirstOrDefault(l => l.Contains("RockwellAutomation", StringComparison.Ordinal));
        if (!string.IsNullOrEmpty(where)) parts.Add(where);
        return string.Join(" <- ", parts);
    }

    static async Task<object> Probe(string name, Func<Task<(bool, string)>> f)
    {
        try { var (ok, detail) = await f(); return new { name, ok, detail }; }
        catch (Exception ex) { return new { name, ok = false, detail = ex.Message }; }
    }

    // InstalledLogixVersions reads the Logix Designer major revisions present
    // on this machine, ascending. Creating a project for a revision that is not
    // installed does NOT fail with "version not installed" — it hangs until the
    // SDK's own timeout, which reads like a licensing failure and is not one.
    static uint[] InstalledLogixVersions()
    {
        var root = @"C:\Program Files (x86)\Rockwell Software\Studio 5000\Logix Designer\ENU";
        if (!Directory.Exists(root)) return [];
        return Directory.GetDirectories(root)
            .Select(Path.GetFileName)
            .Where(n => n is not null && n.StartsWith('v'))
            .Select(n => uint.TryParse(n![1..].Split('.')[0], out var v) ? v : 0u)
            .Where(v => v > 0)
            .Distinct()
            .Order()
            .ToArray();
    }
}
