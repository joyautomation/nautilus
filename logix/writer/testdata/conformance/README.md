# Conformance projects

One small project per mapped instruction family, written to probe the
places where IEC and Logix semantics could differ (logix-authoring.md
§5.5). Each is an ordinary nautilus project with a `target: logix`
section, so the same two commands run it on both runtimes:

    naut test                 # the program, on nautilus, virtual time
    naut test --target logix  # the download, on the controller, real time

The scenarios assert only what EtherNet/IP can see. A one-scan pulse is
invisible to a 100 ms poll, so edge tests latch what they observe; timing
tests leave a margin of several polls around every preset.

All four target the same lab controller (ECHO1's Echo, named DemoLine);
deploying one replaces the previous one.
