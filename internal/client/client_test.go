package client

import "testing"

func TestDaemonBaseURLMissingFileErrors(t *testing.T) {
	t.Setenv("FLUFFLE_HOME", t.TempDir())
	if _, err := DaemonBaseURL(); err == nil {
		t.Fatal("expected error with no daemon.json")
	}
}
