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
	if err := JUnit(&out, sample()); err != nil {
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
	if err := HTML(&out, sample(), "1.2.3"); err != nil {
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
	if err := Markdown(&out, sample()); err != nil {
		t.Fatal(err)
	}
	md := out.String()
	for _, want := range []string{"### ❌ RecoveryLab: orders &lt;script&gt;", "**0 of 1 checks held** · 1 skipped", "| ❌ | Lost response |", "| ➖ | Crash after ack | needs a service |", "<summary>Lost response: why and how to fix</summary>"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown is missing %q:\n%s", want, md)
		}
	}
}
