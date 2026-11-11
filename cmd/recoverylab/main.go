package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/Wali05/recoverylab/internal/audit"
	"github.com/Wali05/recoverylab/internal/config"
	"github.com/Wali05/recoverylab/internal/engine"
	"github.com/Wali05/recoverylab/internal/fixture"
)

// Release builds set this from the Git tag with -ldflags.
var version = "dev"

func main() { os.Exit(run(os.Args[1:])) }

// interrupted is the conventional exit code after Ctrl-C (128 + SIGINT).
const interrupted = 130

func run(args []string) int {
	if len(args) == 0 {
		usage(os.Stderr)
		return 2
	}
	// Ctrl-C cancels the experiment in progress, which also stops a managed
	// service. Without this a killed run could leave the service running.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	switch args[0] {
	case "help", "--help", "-h":
		usage(os.Stdout)
		return 0
	case "version", "--version":
		fmt.Println("RecoveryLab", currentVersion())
		return 0
	case "run":
		return runFile(ctx, args[1:])
	case "init":
		return initScenario(args[1:])
	case "discover":
		return discover(args[1:])
	case "audit":
		return runAudit(ctx, args[1:])
	case "validate":
		return validate(args[1:])
	case "demo":
		return demo(ctx, args[1:])
	case "fixture":
		return serveFixture(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", args[0])
		usage(os.Stderr)
		return 2
	}
}

// flagExit turns a flag parsing result into an exit code: asking for --help
// is not an error.
func flagExit(err error) int {
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	return 2
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

func usage(w io.Writer) {
	fmt.Fprint(w, `RecoveryLab — test whether local HTTP services preserve state through failures

Usage:
  recoverylab demo
  recoverylab discover --base http://127.0.0.1:8000 [--openapi URL|FILE] [--out DIR]
  recoverylab init --url WRITE_URL --state STATE_URL [--body JSON] [--out FILE]
  recoverylab audit [--bursts N] [--crash-points N] [--format text|json] SCENARIO.json|DIR ...
  recoverylab run [--format text|json] [--output REPORT.json] [--repeat N] SCENARIO.json
  recoverylab validate SCENARIO.json
  recoverylab fixture [--mode MODE] [--listen 127.0.0.1:8080] [--journal FILE]
  recoverylab version

Commands:
  demo      Start working and deliberately buggy services and test them all.
  discover  Read your OpenAPI document and write a scenario per write endpoint.
  init      Look at your state endpoint and write a scenario for one endpoint.
  audit     Run every failure that applies to a request and print a report card.
  run       Run only the scenario named in the file.
  validate  Check a scenario file without sending anything.
  fixture   Serve the small test service the demo uses.

Failures:
  lost-response          Hide an acknowledged response, retry, and verify state.
  concurrent-duplicates  Send 2–64 copies at once and verify state.
  key-reuse              Change the body, reuse its key, and check rejection and state.
  crash-after-ack        Kill a managed service after it answers, restart it, and retry.
  crash-mid-request      Kill it while it handles the request; nothing may be left half-done.

Reports:
  audit and run also take --html FILE.html for a report you can share,
  --junit FILE.xml for CI, --markdown FILE.md for a GitHub job summary, and
  --badge FILE.svg for a README badge.

Exit codes: 0 every check held, 1 a check failed, 2 the experiment couldn't finish.
Run "recoverylab COMMAND --help" for a command's flags.
`)
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

func runFile(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	format := fs.String("format", "text", "text or json")
	output := fs.String("output", "", "write JSON report to a file")
	repeat := fs.Int("repeat", 1, "run the scenario 1-100 times with fresh run IDs")
	junit := fs.String("junit", "", "write a JUnit XML report for CI")
	html := fs.String("html", "", "write a self-contained HTML report")
	markdown := fs.String("markdown", "", "write a Markdown summary, such as $GITHUB_STEP_SUMMARY")
	badge := fs.String("badge", "", "write an SVG badge for a README")
	if err := fs.Parse(args); err != nil {
		return flagExit(err)
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
	for i := 0; i < *repeat && ctx.Err() == nil; i++ {
		reports = append(reports, engine.Run(ctx, c))
	}
	if ctx.Err() != nil {
		fmt.Fprintln(os.Stderr, "Interrupted. Any managed service was stopped.")
		return interrupted
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
	if err := writeReports([]audit.Report{audit.FromRuns(c, reports)}, reportPaths{*junit, *html, *markdown, *badge}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if *format == "json" {
		fmt.Println(string(encoded))
	} else if *repeat > 1 {
		printBatch(os.Stdout, newStyle(os.Stdout), batch)
	} else {
		printReport(os.Stdout, newStyle(os.Stdout), reports[0])
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

func freeAddress() (string, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr, nil
}

func serveFixture(args []string) int {
	fs := flag.NewFlagSet("fixture", flag.ContinueOnError)
	listen := fs.String("listen", "127.0.0.1:8080", "listen address")
	mode := fs.String("mode", "correct", "one of: "+strings.Join(fixture.Modes, ", "))
	journal := fs.String("journal", "journal.jsonl", "persistent journal path")
	if err := fs.Parse(args); err != nil {
		return flagExit(err)
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
