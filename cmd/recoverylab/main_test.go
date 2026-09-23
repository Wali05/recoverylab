package main

import (
	"testing"

	"github.com/Wali05/recoverylab/internal/engine"
)

func TestFixtureRejectsNonLoopbackListen(t *testing.T) {
	if got := run([]string{"fixture", "--listen", "0.0.0.0:8080"}); got != 2 {
		t.Fatalf("non-loopback fixture address returned %d, want 2", got)
	}
}

func TestBatchKeepsViolationVisible(t *testing.T) {
	b := summarize([]engine.Report{{Status: "PASS"}, {Status: "VIOLATION"}, {Status: "ERROR"}})
	if b.Status != "VIOLATION" || b.Runs != 3 || b.Passed != 1 || b.Violations != 1 || b.Errors != 1 {
		t.Fatalf("unexpected batch summary: %+v", b)
	}
}
