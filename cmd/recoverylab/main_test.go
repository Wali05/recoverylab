package main

import (
	"strings"
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

func TestTextReportExplainsViolation(t *testing.T) {
	observed := int64(2)
	r := engine.Report{Name: "orders", Scenario: "lost-response", Status: "VIOLATION",
		Checks: []engine.StateCheck{{Name: "orders", Baseline: 0, Expected: 1, Observed: &observed}}}
	var out strings.Builder
	printReport(&out, style{}, r)
	for _, want := range []string{"VIOLATION orders", "✗ orders", "0 → 2", "What happened", "How to fix"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("report is missing %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "\x1b[") {
		t.Error("plain style must not emit escape codes")
	}
}

func TestWrapTextKeepsHangingIndent(t *testing.T) {
	got := wrapText("one two three four", 9, 2)
	if got != "one two\n  three\n  four" {
		t.Fatalf("got %q", got)
	}
}
