// TestSeams.cs: test-only seams, compiled in only with TIAD_TEST_SEAMS.
// build-fake.sh defines it for tiad-fake's own copy of the core
// (tiad-core-seams.dll); the release agent (install-tiad.ps1, tiad.exe)
// never does, so no release build can reach them.
//
// They reach defence in depth that no request can reach since the agent
// holds one session at a time (#1186), so it is tested, not left to rot:
// the download's blind-trust backstop on the TIA thread, and the retire's
// "no session left" guard.

#if TIAD_TEST_SEAMS
namespace Tiad
{
    public static class TestSeams
    {
        // TrustBeforeDownloadDispatch records a blind trust on the TIA
        // thread after a download's pre-check passed and before its TIA
        // work item is queued, as a call queued ahead of it could once:
        // the backstop must refuse it.
        public static volatile bool TrustBeforeDownloadDispatch;

        // ReserveBeforeRetire reserves a session after a close ended and
        // before the retire check, as a create racing the close could: the
        // retire must leave the instance be. The session is dropped once
        // the check returned.
        public static volatile bool ReserveBeforeRetire;
    }
}
#endif
