package explain

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Wali05/recoverylab/internal/config"
	"github.com/Wali05/recoverylab/internal/engine"
	"github.com/Wali05/recoverylab/internal/fixture"
)

func runFixture(t *testing.T, mode string, edit func(*config.Config)) engine.Report {
	t.Helper()
	handler, err := fixture.New(mode, filepath.Join(t.TempDir(), "journal.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	delta := int64(1)
	c := config.Config{
		Name: "explain", Scenario: "lost-response", Timeout: "8s",
		Request: config.Request{Method: "POST", URL: server.URL + "/operations", Headers: map[string]string{"Idempotency-Key": "k-{{run_id}}"}, Body: json.RawMessage(`{"amount":1}`)},
		Observe: config.Observation{URL: server.URL + "/state", Pointer: "/count", ExpectedDelta: &delta},
	}
	edit(&c)
	return engine.Run(context.Background(), c)
}

func TestDiagnosesFixtureBugs(t *testing.T) {
	for _, test := range []struct {
		name, mode string
		edit       func(*config.Config)
		status     string
		want       []string
	}{
		{"healthy retry", "correct", func(*config.Config) {}, "PASS", []string{"exactly once"}},
		{"duplicate on retry", "duplicate-bug", func(*config.Config) {}, "VIOLATION", []string{"second time", "changed by 2", "unique constraint"}},
		{"race", "race-bug", func(c *config.Config) { c.Scenario = "concurrent-duplicates"; c.Concurrency = 8 }, "VIOLATION", []string{"check-then-write race", "ON CONFLICT"}},
		{"changed payload written", "duplicate-bug", func(c *config.Config) { c.Scenario = "key-reuse"; c.ChangedBody = json.RawMessage(`{"amount":2}`) }, "VIOLATION", []string{"another effect", "fingerprint"}},
		{"changed payload rejected", "correct", func(c *config.Config) { c.Scenario = "key-reuse"; c.ChangedBody = json.RawMessage(`{"amount":2}`) }, "PASS", []string{"rejected"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := runFixture(t, test.mode, test.edit)
			if r.Status != test.status {
				t.Fatalf("status %s, want %s: %+v", r.Status, test.status, r)
			}
			d := Report(r)
			text := d.Summary + " " + d.Cause + " " + strings.Join(d.Fixes, " ")
			for _, want := range test.want {
				if !strings.Contains(text, want) {
					t.Errorf("diagnosis %q is missing %q", text, want)
				}
			}
		})
	}
}

func i64(v int64) *int64 { return &v }

func TestDiagnosesCrashOutcomes(t *testing.T) {
	lost := engine.Report{Status: "VIOLATION", Scenario: "crash-after-ack",
		Checks: []engine.StateCheck{{Name: "orders", Baseline: 4, Expected: 5, Observed: i64(4)}}}
	if d := Report(lost); !strings.Contains(d.Summary, "lost the write") || !strings.Contains(d.Fixes[0], "before sending") {
		t.Fatalf("early acknowledgement: %+v", d)
	}

	repaired := engine.Report{Status: "VIOLATION", Scenario: "crash-after-ack",
		AfterRestartChecks: []engine.StateCheck{{Name: "orders", Baseline: 0, Expected: 1, Observed: i64(0)}},
		Checks:             []engine.StateCheck{{Name: "orders", Baseline: 0, Expected: 1, Observed: i64(1)}},
		RetryResponse:      &engine.ResponseCheck{Passed: true}}
	if d := Report(repaired); !strings.Contains(d.Summary, "lost the write") || !strings.Contains(d.Cause, "can't hide this") {
		t.Fatalf("a retry that repairs the write must still be called out: %+v", d)
	}

	forgot := engine.Report{Status: "VIOLATION", Scenario: "crash-after-ack",
		AfterRestartChecks: []engine.StateCheck{{Name: "orders", Baseline: 0, Expected: 1, Observed: i64(1)}},
		Checks:             []engine.StateCheck{{Name: "orders", Baseline: 0, Expected: 1, Observed: i64(2)}},
		RetryResponse:      &engine.ResponseCheck{Passed: true}}
	if d := Report(forgot); !strings.Contains(d.Cause, "forgot") {
		t.Fatalf("keys lost on restart: %+v", d)
	}

	newID := engine.Report{Status: "VIOLATION", Scenario: "crash-after-ack",
		AfterRestartChecks: []engine.StateCheck{{Name: "orders", Baseline: 0, Expected: 1, Observed: i64(1)}},
		Checks:             []engine.StateCheck{{Name: "orders", Baseline: 0, Expected: 1, Observed: i64(1)}},
		RetryResponse:      &engine.ResponseCheck{RetryHTTPStatus: 200, SameJSONPointers: []string{"/id"}, Issues: []string{`retry JSON value at "/id" differs from original or is missing`}}}
	if d := Report(newID); !strings.Contains(d.Summary, "different /id") || !strings.Contains(d.Summary, "after restart") {
		t.Fatalf("changed reply: %+v", d)
	}
}

func TestDiagnosesRetryErrorAndSetupProblems(t *testing.T) {
	conflict := engine.Report{Status: "VIOLATION", Scenario: "lost-response",
		Checks:        []engine.StateCheck{{Name: "state", Expected: 1, Observed: i64(1)}},
		RetryResponse: &engine.ResponseCheck{RetryHTTPStatus: 500, ExpectedStatus: "2xx", Issues: []string{"retry returned HTTP 500; expected 2xx"}}}
	if d := Report(conflict); !strings.Contains(d.Summary, "HTTP 500") || !strings.Contains(d.Cause, "server error") {
		t.Fatalf("retry error: %+v", d)
	}
	for errText, want := range map[string]string{
		`baseline observation "state": dial tcp 127.0.0.1:9: connect: connection refused`: "Nothing is listening",
		`baseline observation "state": value at "/items" is not a JSON integer`:           "measure",
		`baseline observation "state": JSON pointer "/count": key "count" not found`:      "init --state",
		`service did not acknowledge original operation: HTTP 400`:                        "curl",
	} {
		d := Report(engine.Report{Status: "ERROR", Error: errText})
		if !strings.Contains(d.Cause+strings.Join(d.Fixes, " "), want) {
			t.Errorf("%q: diagnosis %+v is missing %q", errText, d, want)
		}
	}
}
