// Whether the pages load the error reporter: only when the build had a DSN
// (#1073). Without one the page has no script tag and nothing is sent.
export const ERRORS_ENABLED = !!process.env.NAUT_ERRORS_DSN;
