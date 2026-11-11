package audit

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Wali05/recoverylab/internal/engine"

	"github.com/Wali05/recoverylab/internal/config"
	"github.com/Wali05/recoverylab/internal/fixture"
)

func base(url string) config.Config {
	delta := int64(1)
	return config.Config{
		Name:    "orders",
		Timeout: "8s",
		Request: config.Request{Method: "POST", URL: url + "/operations", Headers: map[string]string{"Idempotency-Key": "k-{{run_id}}"}, Body: json.RawMessage(`{"amount":1}`)},
		Observe: config.Observation{URL: url + "/state", Pointer: "/count", ExpectedDelta: &delta},
	}
}

func TestPlanCarriesOnlyMatchingSettings(t *testing.T) {
	c := base("http://127.0.0.1:8080")
	c.Scenario = "concurrent-duplicates"
	c.Concurrency = 8
	c.ConcurrentResponse = &config.ConcurrentResponse{AllowedStatuses: []int{201, 409}}
	c.RetryResponse = &config.RetryResponse{SameJSONPointers: []string{"/id"}}
	steps := Plan(c, Options{Bursts: 3, CrashPoints: 4})
	if len(steps) != 5 {
		t.Fatalf("got %d steps", len(steps))
	}
	lost, burst, reuse, crash := steps[0], steps[1], steps[2], steps[3]
	if lost.SkipReason != "" || lost.Config.Concurrency != 0 || lost.Config.ConcurrentResponse != nil || lost.Config.RetryResponse == nil {
		t.Errorf("lost-response step: %+v", lost)
	}
	if burst.SkipReason != "" || burst.Runs != 3 || burst.Config.Workers() != 8 || burst.Config.RetryResponse != nil {
		t.Errorf("concurrent step: %+v", burst)
	}
	if reuse.SkipReason != "" || string(reuse.Config.ChangedBody) != `{"amount":2}` || reuse.Note != "changed /amount from 1 to 2" {
		t.Errorf("key-reuse step: %+v", reuse)
	}
	if !strings.Contains(crash.SkipReason, `"service"`) {
		t.Errorf("crash step should need a service: %+v", crash)
	}

	c.Request.Headers = nil
	if reuse := Plan(c, Options{Bursts: 1})[2]; !strings.Contains(reuse.SkipReason, "Idempotency-Key") {
		t.Errorf("key reuse without a key: %+v", reuse)
	}
	c.Request.Headers = map[string]string{"idempotency-key": "x"}
	c.Service = &config.Service{Command: []string{"svc"}, ReadyURL: "http://127.0.0.1:8080/health"}
	steps = Plan(c, Options{Bursts: 1, CrashPoints: 4})
	if steps[2].SkipReason != "" || steps[3].SkipReason != "" || steps[3].Config.RetryResponse == nil || steps[4].SkipReason != "" || steps[4].Runs != 4 {
		t.Errorf("service and lowercase key header should enable every step: %+v", steps)
	}
}

func TestChangeBodyMakesARealChange(t *testing.T) {
	for _, test := range []struct{ body, want, note string }{
		{`{"sku":"book","quantity":1}`, `{"quantity":2,"sku":"book"}`, "/quantity from 1 to 2"},
		{`{"price":9.5}`, `{"price":10.5}`, "/price from 9.5 to 10.5"},
		{`{"id":"{{run_id}}","sku":"book"}`, `{"id":"{{run_id}}","sku":"book-changed"}`, `/sku from "book" to "book-changed"`},
		{`{"items":[{"gift":false}]}`, `{"items":[{"gift":true}]}`, "/items/0/gift from false to true"},
		{`{"a/b":{"n":0}}`, `{"a/b":{"n":1}}`, "/a~1b/n from 0 to 1"},
		{`{"id":"{{run_id}}","flag":false}`, `{"flag":true,"id":"{{run_id}}"}`, "/flag from false to true"},
		{`{"email":"a+{{run_id}}@example.com"}`, `{"email":"changed-a+{{run_id}}@example.com"}`, `/email from "a+{{run_id}}@example.com" to "changed-a+{{run_id}}@example.com"`},
	} {
		changed, note, err := ChangeBody(json.RawMessage(test.body))
		if err != nil || string(changed) != test.want || !strings.HasSuffix(note, test.note) {
			t.Errorf("%s: got %s, %q, %v", test.body, changed, note, err)
		}
	}
	for _, body := range []string{"", `{}`, `[]`, `null`} {
		if _, _, err := ChangeBody(json.RawMessage(body)); err == nil {
			t.Errorf("%q: expected an error asking for changed_body", body)
		}
	}
}

func TestAuditAgainstFixtures(t *testing.T) {
	for _, test := range []struct {
		mode, status string
		want         map[string]string
	}{
		{"correct", "PASS", map[string]string{"lost-response": "PASS", "concurrent-duplicates": "PASS", "key-reuse": "PASS", "crash-after-ack": "SKIPPED", "crash-mid-request": "SKIPPED"}},
		{"duplicate-bug", "VIOLATION", map[string]string{"lost-response": "VIOLATION", "concurrent-duplicates": "VIOLATION", "key-reuse": "VIOLATION", "crash-after-ack": "SKIPPED", "crash-mid-request": "SKIPPED"}},
	} {
		t.Run(test.mode, func(t *testing.T) {
			handler, err := fixture.New(test.mode, filepath.Join(t.TempDir(), "journal.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(handler)
			defer server.Close()
			c := base(server.URL)
			var streamed int
			report := Run(context.Background(), c, Plan(c, Options{Bursts: 2, CrashPoints: 3}), func(Result) { streamed++ })
			if report.Status != test.status || streamed != 5 || report.Skipped != 2 {
				t.Fatalf("unexpected audit: %+v", report)
			}
			for _, r := range report.Results {
				if r.Status != test.want[r.Scenario] {
					t.Errorf("%s: got %s, want %s", r.Scenario, r.Status, test.want[r.Scenario])
				}
				if r.Status != "SKIPPED" && (r.Diagnosis == nil || r.Diagnosis.Summary == "") {
					t.Errorf("%s has no diagnosis", r.Scenario)
				}
			}
			if burst := report.Results[1]; burst.Runs != 2 || len(burst.Reports) != 2 {
				t.Errorf("bursts were not repeated: %+v", burst)
			}
		})
	}
}

func TestCombineKeepsTheWorstStatus(t *testing.T) {
	s := Combine([]Report{{Status: "PASS", Held: 3, Skipped: 1}, {Status: "ERROR", Held: 2, Errors: 1}, {Status: "VIOLATION", Held: 1, Violations: 2}})
	if s.Status != "VIOLATION" || s.Endpoints != 3 || s.Held != 6 || s.Ran() != 9 || s.Skipped != 1 {
		t.Fatalf("unexpected suite: %+v", s)
	}
	if s := Combine([]Report{{Status: "PASS"}, {Status: "ERROR", Errors: 1}}); s.Status != "ERROR" {
		t.Fatalf("errors without violations: %+v", s)
	}
}

func TestPlanTurnsOnResponseChecks(t *testing.T) {
	steps := Plan(base("http://127.0.0.1:8080"), DefaultOptions)
	if r := steps[0].Config.RetryResponse; r == nil || !r.CompareIDs {
		t.Errorf("lost-response should compare IDs: %+v", r)
	}
	if r := steps[1].Config.ConcurrentResponse; r == nil || !r.NoServerErrors {
		t.Errorf("bursts should reject server errors: %+v", r)
	}
	declared := base("http://127.0.0.1:8080")
	declared.RetryResponse = &config.RetryResponse{SameJSONPointers: []string{"/order_id"}}
	declared.ConcurrentResponse = &config.ConcurrentResponse{AllowedStatuses: []int{201, 409, 500}}
	steps = Plan(declared, DefaultOptions)
	if r := steps[0].Config.RetryResponse; r.CompareIDs || r.SameJSONPointers[0] != "/order_id" {
		t.Errorf("declared pointers should win: %+v", r)
	}
	if r := steps[1].Config.ConcurrentResponse; r.NoServerErrors {
		t.Errorf("a declared status list should win: %+v", r)
	}
}

func TestCrashPointsSpreadAcrossTheRequest(t *testing.T) {
	// With a measured reply time, kills go from "just arrived" up to it.
	var got []string
	for i := 0; i < 4; i++ {
		got = append(got, crashDelay(i, 4, 100*time.Millisecond).String())
	}
	if strings.Join(got, " ") != "0s 25ms 50ms 75ms" {
		t.Errorf("measured: %v", got)
	}
	// Without one, they double.
	got = nil
	for i := 0; i < 6; i++ {
		got = append(got, crashDelay(i, 6, 0).String())
	}
	if strings.Join(got, " ") != "0s 1ms 2ms 4ms 8ms 16ms" {
		t.Errorf("unmeasured: %v", got)
	}
	if d := crashDelay(31, 32, time.Hour); d != 30*time.Second {
		t.Errorf("a kill point must stay inside what a scenario allows: %v", d)
	}

	// A kill that came after the reply shows the service is faster than the
	// estimate, so later kills move earlier.
	prior := []engine.Report{
		{CrashPoint: &engine.CrashPointCheck{DelayMS: 40, ReplyReceived: false}},
		{CrashPoint: &engine.CrashPointCheck{DelayMS: 8, ReplyReceived: true}},
		{},
	}
	if d := replyTime(100*time.Millisecond, prior); d != 8*time.Millisecond {
		t.Errorf("reply time after an early answer: %v", d)
	}
	if d := replyTime(0, prior); d != 8*time.Millisecond {
		t.Errorf("reply time with no estimate: %v", d)
	}
	if d := replyTime(5*time.Millisecond, prior); d != 5*time.Millisecond {
		t.Errorf("a slower reply must not widen the estimate: %v", d)
	}
}

func TestSweepConfigsAreValidScenarios(t *testing.T) {
	c := base("http://127.0.0.1:8080")
	c.Service = &config.Service{Command: []string{"svc"}, ReadyURL: "http://127.0.0.1:8080/health"}
	c.CrashDelay = "9ms" // a setting for run, which the audit replaces
	step := Plan(c, Options{Bursts: 1, CrashPoints: 5})[4]
	if step.Scenario != "crash-mid-request" || step.SkipReason != "" || step.Sweep == nil {
		t.Fatalf("unexpected step: %+v", step)
	}
	for i := 0; i < 5; i++ {
		swept := step.Sweep(i, 333*time.Microsecond, nil)
		if err := swept.Validate(); err != nil {
			t.Fatalf("run %d (%q): %v", i, swept.CrashDelay, err)
		}
		if i > 0 && swept.CrashAfter() <= 0 {
			t.Fatalf("run %d has no delay: %q", i, swept.CrashDelay)
		}
	}
	if off := Plan(c, Options{Bursts: 1, CrashPoints: 0})[4]; !strings.Contains(off.SkipReason, "--crash-points 0") {
		t.Errorf("zero crash points should skip the step: %+v", off)
	}
	c.Service = nil
	if skipped := Plan(c, DefaultOptions)[4]; !strings.Contains(skipped.SkipReason, `"service"`) {
		t.Errorf("no service: %+v", skipped)
	}
}

func TestCrashNoteSaysWhereKillsLanded(t *testing.T) {
	reports := []engine.Report{
		{CrashPoint: &engine.CrashPointCheck{DelayMS: 0.34, ReplyReceived: false}},
		{CrashPoint: &engine.CrashPointCheck{DelayMS: 12.6, ReplyReceived: false}},
		{CrashPoint: &engine.CrashPointCheck{DelayMS: 4, ReplyReceived: true}},
		{Status: "ERROR"},
	}
	if got := crashNote(reports); got != "3 kills, 0.3 to 13 ms in; 2 landed before the reply" {
		t.Errorf("got %q", got)
	}
	if got := crashNote(reports[:1]); got != "1 kill, 0.3 ms in; 1 landed before the reply" {
		t.Errorf("got %q", got)
	}
	if got := crashNote(reports[3:]); got != "" {
		t.Errorf("no kills: %q", got)
	}
}
