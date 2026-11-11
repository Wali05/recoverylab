package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Wali05/recoverylab/internal/audit"
	"github.com/Wali05/recoverylab/internal/config"
)

// demoService is one fixture in the demo: what it does wrong, and what each
// check should say about it. An empty expectation means the outcome depends
// on where a kill happens to land, so the demo shows it without judging it.
type demoService struct {
	mode  string
	about string
	want  [5]string // in demoChecks order
}

var demoChecks = [5]struct{ scenario, head, sub string }{
	{"lost-response", "Lost", "response"},
	{"concurrent-duplicates", "Concurrent", "duplicates"},
	{"key-reuse", "Key", "reuse"},
	{"crash-after-ack", "Crash", "after ack"},
	{"crash-mid-request", "Crash", "mid-request"},
}

const pass, fail = "PASS", "VIOLATION"

var demoServices = []demoService{
	{"correct", "saves the key and the write in one step", [5]string{pass, pass, pass, pass, pass}},
	{"duplicate-bug", "ignores the idempotency key", [5]string{fail, fail, fail, fail, ""}},
	{"race-bug", "looks the key up, then writes, with no lock", [5]string{pass, fail, fail, pass, pass}},
	{"early-ack-bug", "answers 201 before the write is saved", [5]string{fail, fail, fail, fail, fail}},
	{"replay-bug", "answers a retry with a different ID", [5]string{fail, pass, fail, fail, ""}},
	{"torn-write-bug", "saves an operation and its charge separately", [5]string{pass, pass, pass, pass, fail}},
	{"claim-first-bug", "saves the key before the work it stands for", [5]string{pass, pass, pass, pass, fail}},
}

// demo starts every fixture, audits each one, and checks that RecoveryLab
// reached the right verdict in every cell. It doubles as an end-to-end test
// of the release binary.
func demo(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	html := fs.String("html", "", "also write the full HTML report for all seven services")
	if err := fs.Parse(args); err != nil {
		return flagExit(err)
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: recoverylab demo [--html REPORT.html]")
		return 2
	}
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	root, err := os.MkdirTemp("", "recoverylab-demo-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	defer os.RemoveAll(root)

	s := newStyle(os.Stdout)
	out := os.Stdout
	fmt.Fprintf(out, "\n %s\n", s.bold("RecoveryLab demo"))
	fmt.Fprintf(out, " %s\n", s.dim(fmt.Sprintf("%d small services start on this machine: one correct, %d with a planted bug.", len(demoServices), len(demoServices)-1)))
	fmt.Fprintf(out, " %s\n\n", s.dim("Each loses a reply, takes 16 copies at once, has its key reused, and is killed twice."))
	printDemoHead(out, s)

	// The services don't share anything, so they run side by side; rows are
	// still printed in order.
	started := time.Now()
	type outcome struct {
		report audit.Report
		err    error
	}
	results := make([]chan outcome, len(demoServices))
	for i, svc := range demoServices {
		results[i] = make(chan outcome, 1)
		go func(i int, svc demoService) {
			c, err := demoScenario(exe, filepath.Join(root, svc.mode), svc.mode)
			if err != nil {
				results[i] <- outcome{err: err}
				return
			}
			steps := audit.Plan(c, audit.Options{Bursts: 2, CrashPoints: 4})
			results[i] <- outcome{report: audit.Run(ctx, c, steps, nil)}
		}(i, svc)
	}

	reports := make([]audit.Report, 0, len(demoServices))
	var wrong []string
	caught := 0
	for i, svc := range demoServices {
		res := <-results[i]
		if res.err != nil {
			fmt.Fprintln(os.Stderr, res.err)
			return 2
		}
		if ctx.Err() != nil {
			fmt.Fprintln(os.Stderr, "\nInterrupted. Every demo service was stopped.")
			return interrupted
		}
		reports = append(reports, res.report)
		printDemoRow(out, s, svc, res.report)
		found := false
		for j, check := range demoChecks {
			got := res.report.Results[j]
			found = found || got.Status == fail
			if svc.want[j] != "" && got.Status != svc.want[j] {
				detail := ""
				if got.Diagnosis != nil {
					detail = ": " + got.Diagnosis.Summary
				}
				wrong = append(wrong, fmt.Sprintf("%s, %s: got %s, expected %s%s", svc.mode, check.scenario, got.Status, svc.want[j], detail))
			}
		}
		if i > 0 && found {
			caught++
		}
	}
	fmt.Fprintf(out, "\n %s\n", s.dim(fmt.Sprintf("%s held   %s caught   %d experiments in %.1f s", s.green("✓"), s.red("✗"), experiments(reports), time.Since(started).Seconds())))

	if *html != "" {
		if err := writeReports(reports, reportPaths{html: *html}); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
	}
	if len(wrong) > 0 {
		fmt.Fprintf(os.Stderr, "\n Demo failed: RecoveryLab reached the wrong verdict.\n")
		for _, w := range wrong {
			fmt.Fprintln(os.Stderr, "   "+w)
		}
		return 1
	}

	fmt.Fprintf(out, "\n %s %s\n", s.badge("PASS", 0), s.bold(fmt.Sprintf("The correct service held all %d checks, and all %d planted bugs were caught.", len(demoChecks), caught)))
	printDemoFinding(out, s, reports)
	if *html != "" {
		fmt.Fprintf(out, "\n %s %s\n", s.bold("Wrote"), *html)
	}
	fmt.Fprintf(out, "\n %s\n", s.bold("Try it on your own API (start it first):"))
	fmt.Fprintln(out, "   recoverylab discover --base http://127.0.0.1:8000    "+s.dim("# reads your OpenAPI document"))
	fmt.Fprintln(out, "   recoverylab audit recoverylab/")
	if *html == "" {
		fmt.Fprintf(out, "\n %s\n", s.dim("recoverylab demo --html report.html writes the full report for these services."))
	}
	fmt.Fprintln(out)
	return 0
}

// demoScenario describes one fixture as a managed service with two effects
// to watch: the operation, and the charge that belongs to it.
func demoScenario(exe, dir, mode string) (config.Config, error) {
	addr, err := freeAddress()
	if err != nil {
		return config.Config{}, err
	}
	base := "http://" + addr
	one := int64(1)
	return config.Config{
		Name:    mode,
		Timeout: "15s",
		Service: &config.Service{
			Command:        []string{exe, "fixture", "--listen", addr, "--mode", mode, "--journal", filepath.Join(dir, "journal.jsonl")},
			ReadyURL:       base + "/health",
			StartupTimeout: "10s",
		},
		Request: config.Request{Method: "POST", URL: base + "/operations", Headers: map[string]string{"Idempotency-Key": "{{uuid}}"}, Body: json.RawMessage(`{"amount":1}`)},
		Checks: []config.Observation{
			{Name: "operations", URL: base + "/state", Pointer: "/count", ExpectedDelta: &one},
			{Name: "charges", URL: base + "/state", Pointer: "/charges", ExpectedDelta: &one},
		},
	}, nil
}

const demoNameWidth, demoCellWidth = 17, 12

func printDemoHead(w io.Writer, s style) {
	for _, line := range []func(int) string{
		func(i int) string { return demoChecks[i].head },
		func(i int) string { return demoChecks[i].sub },
	} {
		row := strings.Repeat(" ", 2+demoNameWidth)
		for i := range demoChecks {
			row += fmt.Sprintf("%-*s", demoCellWidth, line(i))
		}
		fmt.Fprintln(w, s.dim(strings.TrimRight(row, " ")))
	}
}

func printDemoRow(w io.Writer, s style, svc demoService, r audit.Report) {
	fmt.Fprintf(w, "  %s", s.bold(fmt.Sprintf("%-*s", demoNameWidth, svc.mode)))
	for i := range demoChecks {
		text, paint := "!", s.amber
		switch r.Results[i].Status {
		case pass:
			text, paint = "✓", s.green
		case fail:
			text, paint = "✗ caught", s.red
		}
		pad := ""
		if i < len(demoChecks)-1 {
			// Pad by characters: "✓" is one column wide but three bytes.
			pad = strings.Repeat(" ", demoCellWidth-utf8.RuneCountInString(text))
		}
		fmt.Fprint(w, paint(text)+pad)
	}
	fmt.Fprintf(w, "\n  %s\n", s.dim(strings.Repeat(" ", demoNameWidth)+svc.about))
}

// printDemoFinding shows one explanation in full, so the demo ends with
// what a finding on a real service looks like.
func printDemoFinding(w io.Writer, s style, reports []audit.Report) {
	for _, r := range reports {
		if r.Name != "torn-write-bug" {
			continue
		}
		for _, result := range r.Results {
			if result.Scenario != "crash-mid-request" || result.Status != fail || result.Diagnosis == nil {
				continue
			}
			fmt.Fprintf(w, "\n %s %s\n", s.bold("Every ✗ comes with an explanation."), s.dim("This is torn-write-bug, killed mid-request:"))
			fmt.Fprintln(w)
			printDiagnosis(w, s, *result.Diagnosis)
		}
	}
}

func experiments(reports []audit.Report) int {
	n := 0
	for _, r := range reports {
		for _, result := range r.Results {
			n += result.Runs
		}
	}
	return n
}
