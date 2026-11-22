package main

import (
	"testing"

	"github.com/joyautomation/nautilus/internal/project"
)

// The controller links the Postgres journal and the page senders that the
// manifest no longer imports (#1239). Dropping the registration would leave
// naut run refusing sink: postgres and every paging channel as if it were
// the playground, and no other test runs Postgres in CI.
func TestControllerRegistersTheAlarmParts(t *testing.T) {
	postgres, senders := project.AlarmSeams()
	if !postgres {
		t.Error("cmd/naut registers no Postgres alarm journal: naut run would refuse alarms.journal.sink: postgres")
	}
	if !senders {
		t.Error("cmd/naut registers no alarm senders: naut run would refuse every alarms.channels kind")
	}
}
