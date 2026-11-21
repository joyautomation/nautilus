package main

// The IANA time zone database, built in: alarm escalation's on-call
// rosters and `when:` windows name a zone (alarms.oncall.timezone, #715),
// and Windows and small embedded images have no zoneinfo of their own, so
// `naut check`, the language server and `naut run` would otherwise refuse
// a valid manifest there.
import _ "time/tzdata"
