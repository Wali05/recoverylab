package report

import (
	"encoding/xml"
	"strings"
	"testing"
	"time"

	"github.com/Wali05/recoverylab/internal/audit"
	"github.com/Wali05/recoverylab/internal/engine"
	"github.com/Wali05/recoverylab/internal/explain"
)

func sample() audit.Report {
	observed := int64(2)
	start := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	bad := engine.Report{Name: "orders", Scenario: "lost-response", RunID: "abc", Status: "VIOLATION", StartedAt: start.Format(time.RFC3339Nano), DurationMS: 40,
		Checks:   []engine.StateCheck{{Name: "state", Baseline: 0, Expected: 1, Observed: &observed}},
		Attempts: []engine.Attempt{{Kind: "original", ClientError: "EOF"}, {Kind: "retry", HTTPStatus: 201}},
		Events:   []engine.Event{{At: start.Add(12 * time.Millisecond).Format(time.RFC3339Nano), Step: "retry", Detail: "resent"}},
		Error:    "check failed: state expected 1, observed 2"}
	d := explain.Report(bad)
	return audit.Report{Name: `orders <script>alert(1)</script>`, Method: "POST", URL: "http://127.0.0.1:8080/orders", Status: "VIOLATION", Violations: 1, Skipped: 1,
		Results: []audit.Result{
			{Scenario: "lost-response", Title: "Lost response", Status: "VIOLATION", Runs: 1, Violations: 1, Diagnosis: &d, Reports: []engine.Report{bad}},
			{Scenario: "crash-after-ack", Title: "Crash after ack", Status: "SKIPPED", Reason: "needs a service"},
		}}
}

func TestJUnitCountsAndExplains(t *testing.T) {
	var out strings.Builder
	if err := JUnit(&out, []audit.Report{sample()}); err != nil {
		t.Fatal(err)
	}
	var parsed junitSuites
	if err := xml.Unmarshal([]byte(out.String()), &parsed); err != nil {
		t.Fatalf("invalid XML: %v\n%s", err, out.String())
	}
	if parsed.Tests != 2 || parsed.Failures != 1 || parsed.Skipped != 1 || len(parsed.Suites) != 2 {
		t.Fatalf("unexpected totals: %+v", parsed)
	}
	failure := parsed.Suites[0].Cases[0].Failure
	if failure == nil || !strings.Contains(failure.Text, "How to fix") || !strings.Contains(failure.Message, "observed 2") {
		t.Fatalf("failure lacks the explanation: %+v", failure)
	}
}

func TestHTMLIsSelfContainedAndEscaped(t *testing.T) {
	var out strings.Builder
	if err := HTML(&out, []audit.Report{sample()}, "1.2.3"); err != nil {
		t.Fatal(err)
	}
	page := out.String()
	for _, want := range []string{"0 of 1 check held", "1 skipped", "How to fix", "12 ms", "no reply, HTTP 201", "needs a service", "RecoveryLab 1.2.3", "&lt;script&gt;"} {
		if !strings.Contains(page, want) {
			t.Errorf("page is missing %q", want)
		}
	}
	for _, banned := range []string{"<script>", "http-equiv", "<link", "src=\"http"} {
		if strings.Contains(page, banned) {
			t.Errorf("page contains %q", banned)
		}
	}
}

func TestMarkdownSummary(t *testing.T) {
	var out strings.Builder
	if err := Markdown(&out, []audit.Report{sample()}); err != nil {
		t.Fatal(err)
	}
	md := out.String()
	for _, want := range []string{"### ❌ RecoveryLab: orders &lt;script&gt;", "**0 of 1 checks held** · 1 skipped", "| ❌ | Lost response |", "| ➖ | Crash after ack | needs a service |", "<summary>Lost response: why and how to fix</summary>"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown is missing %q:\n%s", want, md)
		}
	}
}

func TestSeveralEndpoints(t *testing.T) {
	second := sample()
	second.Name, second.URL, second.Status, second.Violations, second.Held = "customers", "http://127.0.0.1:8080/customers", "PASS", 0, 1
	second.Results = second.Results[:1]
	second.Results[0].Status = "PASS"
	audits := []audit.Report{sample(), second}

	var page strings.Builder
	if err := HTML(&page, audits, "1.2.3"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"2 endpoints", "1 of 2 checks held", `href="#endpoint-1"`, `id="endpoint-1"`, "http://127.0.0.1:8080/customers"} {
		if !strings.Contains(page.String(), want) {
			t.Errorf("page is missing %q", want)
		}
	}

	var md strings.Builder
	if err := Markdown(&md, audits); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(md.String(), "### ❌ RecoveryLab: 2 endpoints · 1 of 2 checks held") || !strings.Contains(md.String(), "#### ✅ customers") {
		t.Errorf("markdown:\n%s", md.String())
	}

	var xmlOut strings.Builder
	if err := JUnit(&xmlOut, audits); err != nil {
		t.Fatal(err)
	}
	var parsed junitSuites
	if err := xml.Unmarshal([]byte(xmlOut.String()), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Name != "RecoveryLab: 2 endpoints" || len(parsed.Suites) != 3 || parsed.Suites[2].Name != "customers: Lost response" {
		t.Fatalf("junit: %+v", parsed)
	}
}

func TestBadge(t *testing.T) {
	var out strings.Builder
	if err := Badge(&out, []audit.Report{sample()}); err != nil {
		t.Fatal(err)
	}
	svg := out.String()
	if !strings.Contains(svg, `aria-label="retry safety: 0/1 held"`) || !strings.Contains(svg, "#d73a49") {
		t.Fatalf("badge:\n%s", svg)
	}
	if err := xml.Unmarshal([]byte(svg), new(struct{})); err != nil {
		t.Fatalf("badge is not valid XML: %v", err)
	}
}

func TestDiagramDrawsWhatTheRunRecorded(t *testing.T) {
	a := sample()
	run := a.Results[0].Reports[0]
	run.FaultInjected = true
	run.RetryResponse = &engine.ResponseCheck{OriginalHTTPStatus: 201, RetryHTTPStatus: 201, Passed: true}
	svg := string(diagram(a, run))
	for _, want := range []string{"<svg", "POST /orders", "HTTP 201", "reply lost on the way", "same request again", "state 0 → 2, expected 1", `aria-label="client sends POST /orders`} {
		if !strings.Contains(svg, want) {
			t.Errorf("lost-response diagram is missing %q", want)
		}
	}
	if err := xml.Unmarshal([]byte(svg), new(struct{})); err != nil {
		t.Fatalf("diagram is not well-formed: %v", err)
	}

	zero, one := int64(0), int64(1)
	torn := engine.Report{Scenario: "crash-mid-request", Status: "VIOLATION", FaultInjected: true,
		Attempts:   []engine.Attempt{{Kind: "original", ClientError: "EOF"}, {Kind: "retry", HTTPStatus: 200}},
		Events:     []engine.Event{{Step: "recovery"}},
		CrashPoint: &engine.CrashPointCheck{DelayMS: 0.36, Outcome: "partial"},
		AfterRestartChecks: []engine.StateCheck{
			{Name: "orders", Baseline: 0, Expected: 1, Observed: &one, Allowed: []int64{0, 1}},
			{Name: `<b>charges</b>`, Baseline: 0, Expected: 1, Observed: &zero, Allowed: []int64{0, 1}}},
		Checks: []engine.StateCheck{{Name: "orders", Baseline: 0, Expected: 1, Observed: &one}}}
	svg = string(diagram(a, torn))
	for _, want := range []string{"process killed", "0.4 ms after the request", "connection died, no reply", "restarted", "half-finished", "orders 0 → 1", "&lt;b&gt;charges&lt;/b&gt; 0 → 0"} {
		if !strings.Contains(svg, want) {
			t.Errorf("crash diagram is missing %q", want)
		}
	}
	if strings.Contains(svg, "<b>") {
		t.Error("a check name reached the page unescaped")
	}
	// An experiment that never sent anything has nothing to draw.
	if got := diagram(a, engine.Report{Scenario: "lost-response", Status: "ERROR"}); got != "" {
		t.Errorf("empty run drew %q", got)
	}
}

func TestRunsTableShowsWhereEachKillLanded(t *testing.T) {
	rows := runs(audit.Result{Reports: []engine.Report{
		{Status: "PASS", CrashPoint: &engine.CrashPointCheck{DelayMS: 0, Outcome: "not-applied"}},
		{Status: "PASS", CrashPoint: &engine.CrashPointCheck{DelayMS: 12.4, ReplyReceived: true, Outcome: "applied"}},
		{Status: "ERROR", Error: "connection refused"},
	}})
	if len(rows) != 3 || rows[0].Detail != "killed 0 ms in, before any reply · nothing written" || rows[1].Detail != "killed 12 ms in, after the reply · write all there" || rows[2].Detail != "connection refused" {
		t.Fatalf("rows: %+v", rows)
	}
	if runs(audit.Result{Reports: []engine.Report{{}}}) != nil {
		t.Error("a single run needs no table")
	}
}
