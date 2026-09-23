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
	"strings"
	"time"

	"recoverylab/internal/config"
	"recoverylab/internal/engine"
	"recoverylab/internal/fixture"
)

const version = "0.1.0"

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
		fmt.Println("RecoveryLab", version)
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

func usage() {
	fmt.Println(`RecoveryLab — test whether local HTTP services preserve state through failures

Usage:
  recoverylab demo
  recoverylab validate SCENARIO.json
  recoverylab run [--format text|json] [--output REPORT.json] SCENARIO.json
  recoverylab version

Scenarios:
  lost-response    Hide an acknowledged response, retry, and verify state.
  concurrent-duplicates  Send 2–64 copies at once and verify state.
  crash-after-ack  Kill and restart a managed service after acknowledgement.

Run "recoverylab demo" for a self-contained proof with working and buggy services.
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
	c, err := config.Load(fs.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid scenario:", err)
		return 2
	}
	r := engine.Run(context.Background(), c)
	encoded, err := json.MarshalIndent(r, "", "  ")
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
	} else {
		printReport(r)
	}
	switch r.Status {
	case "PASS":
		return 0
	case "VIOLATION":
		return 1
	default:
		return 2
	}
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
		fmt.Printf("%-38s %-10s expected=%s", r.Name, r.Status, item.want)
		if r.Expected != nil && r.Observed != nil {
			fmt.Printf("  state=%d/%d", *r.Observed, *r.Expected)
		}
		fmt.Println()
		if r.Status != item.want {
			allGood = false
			if r.Error != "" {
				fmt.Println("  ", r.Error)
			}
		}
	}
	if !allGood {
		fmt.Fprintln(os.Stderr, "Demo did not meet its expected outcomes.")
		return 1
	}
	fmt.Println("\nDemo passed: healthy cases held; duplicate, race, and durability defects were detected.")
	return 0
}

func serveFixture(args []string) int {
	fs := flag.NewFlagSet("fixture", flag.ContinueOnError)
	listen := fs.String("listen", "127.0.0.1:8080", "listen address")
	mode := fs.String("mode", "correct", "correct, duplicate-bug, race-bug, or early-ack-bug")
	journal := fs.String("journal", "journal.jsonl", "persistent journal path")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
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
