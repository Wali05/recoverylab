package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	"github.com/Wali05/recoverylab/internal/config"
	"github.com/Wali05/recoverylab/internal/engine"
	"github.com/Wali05/recoverylab/internal/fixture"
)

// Release builds set this from the Git tag with -ldflags.
var version = "dev"

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	if len(args) == 0 {
		usage()
		return 2
	}
	switch args[0] {
	case "help", "--help", "-h":
		usage()
		return 0
	case "version", "--version":
		fmt.Println("RecoveryLab", currentVersion())
		return 0
	case "run":
		return runFile(args[1:])
	case "validate":
		return validate(args[1:])
	case "demo":
		return demo(args[1:])
	case "fixture":
		return serveFixture(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", args[0])
		usage()
		return 2
	}
}

func currentVersion() string {
	if version != "dev" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return strings.TrimPrefix(info.Main.Version, "v")
	}
	return version
}

func usage() {
	fmt.Println(`RecoveryLab — test whether local HTTP services preserve state through failures

Usage:
  recoverylab demo
  recoverylab validate SCENARIO.json
  recoverylab run [--format text|json] [--output REPORT.json] [--repeat N] SCENARIO.json
  recoverylab version

Scenarios:
  lost-response    Hide an acknowledged response, retry, and verify state.
  concurrent-duplicates  Send 2–64 copies at once and verify state.
  crash-after-ack  Kill and restart a managed service; optionally retry afterward.

Run "recoverylab demo" for a self-contained example with working and buggy services.
See examples/ and README.md to test your own local service.`)
}

func validate(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: recoverylab validate SCENARIO.json")
		return 2
	}
	c, err := config.Load(args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid scenario:", err)
		return 2
	}
	fmt.Printf("Valid: %s (%s)\n", c.Name, c.Scenario)
	return 0
}

func runFile(args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	format := fs.String("format", "text", "text or json")
	output := fs.String("output", "", "write JSON report to a file")
	repeat := fs.Int("repeat", 1, "run the scenario 1-100 times with fresh run IDs")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: recoverylab run [flags] SCENARIO.json")
		return 2
	}
	if *format != "text" && *format != "json" {
		fmt.Fprintln(os.Stderr, "--format must be text or json")
		return 2
	}
	if *repeat < 1 || *repeat > 100 {
		fmt.Fprintln(os.Stderr, "--repeat must be between 1 and 100")
		return 2
	}
	c, err := config.Load(fs.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid scenario:", err)
		return 2
	}
	reports := make([]engine.Report, 0, *repeat)
	for i := 0; i < *repeat; i++ {
		reports = append(reports, engine.Run(context.Background(), c))
	}
	batch := summarize(reports)
	var payload any = reports[0]
	if *repeat > 1 {
		payload = batch
	}
	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, "encode report:", err)
		return 2
	}
	if *output != "" {
		if err := os.WriteFile(*output, append(encoded, '\n'), 0644); err != nil {
			fmt.Fprintln(os.Stderr, "write report:", err)
			return 2
		}
	}
	if *format == "json" {
		fmt.Println(string(encoded))
	} else if *repeat > 1 {
		printBatch(batch)
	} else {
		printReport(reports[0])
	}
	switch batch.Status {
	case "PASS":
		return 0
	case "VIOLATION":
		return 1
	default:
		return 2
	}
}

type BatchReport struct {
	Status     string          `json:"status"`
	Runs       int             `json:"runs"`
	Passed     int             `json:"passed"`
	Violations int             `json:"violations"`
	Errors     int             `json:"errors"`
	Reports    []engine.Report `json:"reports"`
}

func summarize(reports []engine.Report) BatchReport {
	b := BatchReport{Status: "PASS", Runs: len(reports), Reports: reports}
	for _, r := range reports {
		switch r.Status {
		case "PASS":
			b.Passed++
		case "VIOLATION":
			b.Violations++
		default:
			b.Errors++
		}
	}
	if b.Violations > 0 {
		b.Status = "VIOLATION"
	} else if b.Errors > 0 {
		b.Status = "ERROR"
	}
	return b
}

func printBatch(b BatchReport) {
	fmt.Printf("\nRECOVERYLAB  %s  %d runs\n", b.Status, b.Runs)
	for i, r := range b.Reports {
		fmt.Printf("%3d  %-10s run=%s", i+1, r.Status, r.RunID)
		if r.Error != "" {
			fmt.Printf("  %s", r.Error)
		}
		fmt.Println()
	}
	fmt.Printf("\nPassed: %d  Violations: %d  Errors: %d\n\n", b.Passed, b.Violations, b.Errors)
}

func printReport(r engine.Report) {
	fmt.Printf("\nRECOVERYLAB  %s  %s\n", r.Status, r.Name)
	fmt.Printf("Scenario: %s    Run: %s    Duration: %d ms\n", r.Scenario, r.RunID, r.DurationMS)
	fmt.Println(strings.Repeat("─", 68))
	for _, e := range r.Events {
		fmt.Printf("%-12s %s\n", e.Step, e.Detail)
	}
	if r.Baseline != nil && r.Observed != nil {
		fmt.Printf("\nState: before=%d  expected=%d  observed=%d\n", *r.Baseline, *r.Expected, *r.Observed)
	} else if len(r.Checks) > 1 {
		fmt.Println()
		for _, check := range r.Checks {
			if check.Observed != nil {
				fmt.Printf("%-16s before=%d  expected=%d  observed=%d\n", check.Name, check.Baseline, check.Expected, *check.Observed)
			}
		}
	}
	if r.Error != "" {
		fmt.Println("\n" + r.Error)
	}
	fmt.Println()
}

func freeAddress() (string, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr, nil
}

func demo(args []string) int {
	if len(args) != 0 {
		fmt.Fprintln(os.Stderr, "usage: recoverylab demo")
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
	cases := []struct{ mode, scenario, want string }{
		{"correct", "lost-response", "PASS"},
		{"duplicate-bug", "lost-response", "VIOLATION"},
		{"correct", "concurrent-duplicates", "PASS"},
		{"race-bug", "concurrent-duplicates", "VIOLATION"},
		{"correct", "crash-after-ack", "PASS"},
		{"early-ack-bug", "crash-after-ack", "VIOLATION"},
	}
	allGood := true
	reports := make([]engine.Report, len(cases))
	for i, item := range cases {
		addr, err := freeAddress()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		base := "http://" + addr
		caseDir := filepath.Join(root, fmt.Sprintf("case-%d", i))
		delta := int64(1)
		c := config.Config{
			Name: item.mode + "/" + item.scenario, Scenario: item.scenario, Timeout: "15s", Concurrency: 16,
			Service: &config.Service{Command: []string{exe, "fixture", "--listen", addr, "--mode", item.mode, "--journal", filepath.Join(caseDir, "journal.jsonl")}, ReadyURL: base + "/health", StartupTimeout: "5s"},
			Request: config.Request{Method: "POST", URL: base + "/operations", Headers: map[string]string{"Idempotency-Key": "recoverylab-{{run_id}}"}, Body: json.RawMessage(`{"amount":1}`)},
			Observe: config.Observation{URL: base + "/state", Pointer: "/count", ExpectedDelta: &delta},
		}
		if item.scenario != "concurrent-duplicates" {
			c.Concurrency = 0
		}
		r := engine.Run(context.Background(), c)
		reports[i] = r
		if r.Status != item.want {
			allGood = false
			fmt.Fprintf(os.Stderr, "%s: got %s, expected %s\n", r.Name, r.Status, item.want)
			if r.Error != "" {
				fmt.Fprintln(os.Stderr, "  ", r.Error)
			}
		}
	}
	fmt.Println("A write succeeds, its reply vanishes, and the client retries. (observed/expected)")
	fmt.Printf("Lost response: correct %s | duplicate bug %s\n", demoResult(reports[0]), demoResult(reports[1]))
	fmt.Printf("16 requests at once: correct %s | race bug %s\n", demoResult(reports[2]), demoResult(reports[3]))
	fmt.Printf("Crash after 2xx: correct %s | early ack bug %s\n", demoResult(reports[4]), demoResult(reports[5]))
	if !allGood {
		fmt.Fprintln(os.Stderr, "Demo failed: at least one fixture missed its expected outcome.")
		return 1
	}
	fmt.Println("Demo passed: 3 healthy cases held; 3 injected defects were detected.")
	return 0
}

func demoResult(r engine.Report) string {
	if r.Observed == nil || r.Expected == nil {
		return r.Status
	}
	return fmt.Sprintf("%s %d/%d", r.Status, *r.Observed, *r.Expected)
}

func serveFixture(args []string) int {
	fs := flag.NewFlagSet("fixture", flag.ContinueOnError)
	listen := fs.String("listen", "127.0.0.1:8080", "listen address")
	mode := fs.String("mode", "correct", "correct, duplicate-bug, race-bug, early-ack-bug, replay-bug, or repair-after-crash-bug")
	journal := fs.String("journal", "journal.jsonl", "persistent journal path")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		return 2
	}
	host, _, err := net.SplitHostPort(*listen)
	ip := net.ParseIP(host)
	if err != nil || (!strings.EqualFold(host, "localhost") && (ip == nil || !ip.IsLoopback())) {
		fmt.Fprintln(os.Stderr, "fixture --listen must use a loopback address such as 127.0.0.1:8080")
		return 2
	}
	handler, err := fixture.New(*mode, *journal)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	server := &http.Server{Addr: *listen, Handler: handler, ReadHeaderTimeout: 3 * time.Second}
	if err := server.ListenAndServe(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	return 0
}
