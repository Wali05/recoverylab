package audit

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

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
	steps := Plan(c, 3)
	if len(steps) != 4 {
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
	if reuse := Plan(c, 1)[2]; !strings.Contains(reuse.SkipReason, "Idempotency-Key") {
		t.Errorf("key reuse without a key: %+v", reuse)
	}
	c.Request.Headers = map[string]string{"idempotency-key": "x"}
	c.Service = &config.Service{Command: []string{"svc"}, ReadyURL: "http://127.0.0.1:8080/health"}
	steps = Plan(c, 1)
	if steps[2].SkipReason != "" || steps[3].SkipReason != "" || steps[3].Config.RetryResponse == nil {
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
	} {
		changed, note, err := ChangeBody(json.RawMessage(test.body))
		if err != nil || string(changed) != test.want || !strings.HasSuffix(note, test.note) {
			t.Errorf("%s: got %s, %q, %v", test.body, changed, note, err)
		}
	}
	for _, body := range []string{"", `{}`, `{"id":"{{run_id}}"}`, `null`} {
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
		{"correct", "PASS", map[string]string{"lost-response": "PASS", "concurrent-duplicates": "PASS", "key-reuse": "PASS", "crash-after-ack": "SKIPPED"}},
		{"duplicate-bug", "VIOLATION", map[string]string{"lost-response": "VIOLATION", "concurrent-duplicates": "VIOLATION", "key-reuse": "VIOLATION", "crash-after-ack": "SKIPPED"}},
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
			report := Run(context.Background(), c, Plan(c, 2), func(Result) { streamed++ })
			if report.Status != test.status || streamed != 4 || report.Skipped != 1 {
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
