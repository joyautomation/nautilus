package tiad

import (
	"strings"
	"testing"
)

// TestDeployStatusOf is the agent's DeployStatus.Of table (RulesTest.cs
// DeployStatusOf): only the kinds that say nothing was loaded are
// not-loaded; anything else is in doubt.
func TestDeployStatusOf(t *testing.T) {
	want := map[string]string{
		DeployLoaded: DeployLoaded, KindDecisionRequired: DeployNotLoaded, KindDeclined: DeployNotLoaded, KindBadRequest: DeployNotLoaded,
		KindTargetRefused: DeployNotLoaded, KindBusy: DeployNotLoaded, KindNotCompiled: DeployNotLoaded, KindOperationFailed: DeployNotLoaded,
		KindOutcomeUnknown: DeployInDoubt, KindNotFound: DeployInDoubt, KindGate: DeployInDoubt, KindUncleanSession: DeployInDoubt, "error": DeployInDoubt, "": DeployInDoubt,
	}
	for k, w := range want {
		if got := DeployStatusOf(k); got != w {
			t.Errorf("DeployStatusOf(%q) = %q, want %q", k, got, w)
		}
	}
}

// TestDeploySourceValidate is the agent's DeploySource.Validate table
// (RulesTest.cs DeploySourceShape).
func TestDeploySourceValidate(t *testing.T) {
	c40 := "0123456789abcdef0123456789abcdef01234567"
	h := "sha256:" + c40 + "0123456789abcdef01234567"
	for _, c := range []struct{ commit, hash, want string }{
		{c40, "", ""},
		{c40 + "0123456789abcdef01234567", h, ""},
		{"", "", "source.commit"},
		{c40[1:], "", "source.commit"},
		{strings.ToUpper(c40), "", "source.commit"},
		{c40[1:] + "g", "", "source.commit"},
		{c40 + "0", "", "source.commit"},
		{c40, h[7:], "source.hash"},
		{c40, "SHA256:" + h[7:], "source.hash"},
		{c40, h + "0", "source.hash"},
		{c40, h[:len(h)-1] + "G", "source.hash"},
	} {
		err := DeploySource{Commit: c.commit, Hash: c.hash}.Validate()
		if c.want == "" && err != nil || c.want != "" && (err == nil || !strings.HasPrefix(err.Error(), c.want)) {
			t.Errorf("source %q %q: %v, want %q", c.commit, c.hash, err, c.want)
		}
	}
	// A download carries the source's error.
	r := DownloadRequest{Address: "192.0.2.10", Software: true, Announced: true, Source: &DeploySource{Commit: "main"}}
	if err := r.Validate(); err == nil || !strings.Contains(err.Error(), "download: source.commit") {
		t.Fatalf("a download with a bad source: %v", err)
	}
}
