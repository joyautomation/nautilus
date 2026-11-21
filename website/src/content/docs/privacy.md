---
title: Privacy
description: What this site and the browser playground collect, and what they never send.
---

nautilus is open source, and this site is a documentation site. It collects
two things, and nothing else.

## Visit statistics

Pages report to Google Analytics (the same property as
[joyautomation.com](https://joyautomation.com)), which counts visits per page.
Block `googletagmanager.com` if you would rather it did not.

## JavaScript errors, to a server we run

When a page or the [playground](/playground/) throws an uncaught JavaScript
error, your browser sends a short report to our own error-monitoring server
at `errors.joyautomation.com` (GlitchTip), so we hear about breakage before
you have to tell us. No third party receives it.

**A report contains:**

- the error's type and the first line of its message (at most 200 characters,
  long quoted strings replaced);
- the stack: function names, line and column numbers, and the script's path;
- the address of the page **without** its query string or `#` fragment, so the
  path only (a saved playground project's id is replaced too);
- your browser and operating system name and version;
- the site's build, and whether the error came from the docs or the playground.

**A report never contains:**

- your IP address: your browser does not send one in the report, and the
  server is set to scrub the address it sees the report come from;
- your projects, code, tag values, or anything you typed or pasted in the
  playground: a project shared by link lives in the `#fragment`, which is
  removed before anything is sent;
- cookies, the page you came from, or any identifier for you;
- what was on the page, or what you clicked, or console output: the
  usual "breadcrumbs" trail is switched off;
- performance traces, session recordings or counts of your visits.

Reports are kept for 30 days, then deleted. The code that decides what is sent
is [`website/src/errors/`](https://github.com/joyautomation/nautilus/tree/main/website/src/errors)
in the repository, with tests that fail if a report ever carries more.

The playground itself runs entirely in your browser; nothing you build in it
leaves the browser unless you press Share (which saves the project to this
site so a link can open it).
