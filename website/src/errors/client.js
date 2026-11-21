// The error reporter bundled into the docs site and the playground
// (scripts/build-errors.mjs -> /errors/errors.js), #1073. JavaScript errors
// only: no breadcrumbs, no performance tracing, no sessions, no replay, no
// client reports, no user, and every event passes scrub.mjs last. Each "no"
// below is an SDK option or an integration we do not add; the scrubber
// backs the ones an event could still carry.
import {
	init,
	globalHandlersIntegration,
	linkedErrorsIntegration,
	dedupeIntegration,
	inboundFiltersIntegration,
	httpContextIntegration
} from '@sentry/browser';
import { scrubEvent } from './scrub.mjs';

// Replaced by scripts/build-errors.mjs; with no DSN the bundle is not built.
const DSN = __NAUT_ERRORS_DSN__;
const RELEASE = __NAUT_ERRORS_RELEASE__;

const here = new URL(location.href);
const app = here.pathname.startsWith('/playground') ? 'playground' : 'docs';

init({
	dsn: DSN,
	release: RELEASE,
	environment: 'production',
	// Errors only, all of them.
	sampleRate: 1.0,
	// No tracing: no tracesSampleRate and no browserTracingIntegration. No
	// replay: no replayIntegration. No sessions: no browserSessionIntegration.
	// No breadcrumbs (console text, DOM clicks, fetch URLs): the breadcrumbs
	// integration is not in the list below, and these two keep it that way.
	maxBreadcrumbs: 0,
	beforeBreadcrumb: () => null,
	// No "client report" envelopes about dropped events.
	sendClientReports: false,
	// The SDK's own IP/cookie/header collection.
	sendDefaultPii: false,
	// Only what we add: the global handlers (uncaught errors and unhandled
	// rejections), cause chains, de-duplication, the page URL and User-Agent
	// (scrubbed to the path), and the filters below.
	defaultIntegrations: false,
	integrations: [
		globalHandlersIntegration(),
		linkedErrorsIntegration(),
		dedupeIntegration(),
		httpContextIntegration(),
		inboundFiltersIntegration({
			// Our own scripts only: a browser extension's or another site's
			// error is not ours to report, and "Script error." says nothing.
			allowUrls: [here.origin],
			ignoreErrors: ['Script error.', 'Script error', /^ResizeObserver loop/]
		})
	],
	initialScope: { tags: { app } },
	beforeSend: (event) => scrubEvent(event, { base: location.href, userAgent: navigator.userAgent })
});
