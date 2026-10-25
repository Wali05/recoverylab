package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/Wali05/recoverylab/internal/config"
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

func TestCandidatesPreferCountsOverPages(t *testing.T) {
	var root any
	dec := json.NewDecoder(strings.NewReader(`{"data":[{"id":1,"count":9}],"page":1,"meta":{"total":1},"version":3}`))
	dec.UseNumber()
	if err := dec.Decode(&root); err != nil {
		t.Fatal(err)
	}
	found := candidates(root, "", 0)
	sort.SliceStable(found, func(i, j int) bool { return found[i].rank > found[j].rank })
	if len(found) != 4 || found[0].pointer != "/meta/total" || found[1].pointer != "/data" || found[1].measure != config.MeasureLength {
		t.Fatalf("unexpected ranking: %+v", found)
	}
	if got := scenarioName("PUT", "http://127.0.0.1:8080/api/v1/Orders/{{run_id}}"); got != "put-api-v1-orders-run-id" {
		t.Fatalf("scenario name %q", got)
	}
}

func TestInitWritesARunnableScenario(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"id":1},{"id":2}]`))
	}))
	defer server.Close()
	out := filepath.Join(t.TempDir(), "scenario.json")
	if code := run([]string{"init", "--url", server.URL + "/orders", "--state", server.URL + "/orders", "--body", `{"sku":"book"}`, "--out", out}); code != 0 {
		t.Fatalf("init exited %d", code)
	}
	c, err := config.Load(out)
	if err != nil {
		t.Fatal(err)
	}
	if c.Observe.Pointer != "" || c.Observe.Measure != config.MeasureLength || c.Request.Headers["Idempotency-Key"] != "recoverylab-{{run_id}}" {
		t.Fatalf("unexpected scenario: %+v", c)
	}
	if code := run([]string{"init", "--url", server.URL + "/orders", "--state", server.URL + "/orders", "--out", out}); code != 2 {
		t.Fatal("init overwrote an existing file without --force")
	}
}
