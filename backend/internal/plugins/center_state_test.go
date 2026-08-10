package plugins

import (
	"path/filepath"
	"testing"
)

func TestCenterKeepsMutableStateOutsideProtectedPayload(t *testing.T) {
	payload := filepath.Join(t.TempDir(), "verified-payload")
	state := filepath.Join(t.TempDir(), "mutable-state")
	c := NewCenterWithState(payload, state)

	if got, want := c.runLogDir(), filepath.Join(state, "runlogs"); got != want {
		t.Fatalf("run log dir=%q want %q", got, want)
	}
	if got, want := c.ezSolverVenvDir(), filepath.Join(state, "ezsolver", ".venv"); got != want {
		t.Fatalf("EzSolver venv dir=%q want %q", got, want)
	}
	if filepath.Clean(c.runLogDir()) == filepath.Clean(filepath.Join(payload, "plugins", ".runlogs")) {
		t.Fatal("mutable logs still live inside the immutable protected payload")
	}
}
