package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/Wali05/recoverylab/internal/audit"
	"github.com/Wali05/recoverylab/internal/config"
	"github.com/Wali05/recoverylab/internal/report"
)

func runAudit(args []string) int {
	fs := flag.NewFlagSet("audit", flag.ContinueOnError)
	format := fs.String("format", "text", "text or json")
	output := fs.String("output", "", "write the JSON audit to a file")
	bursts := fs.Int("bursts", 5, "concurrent bursts to try, 1-100; races don't show up every time")
	junit := fs.String("junit", "", "write a JUnit XML report for CI")
	html := fs.String("html", "", "write a self-contained HTML report")
	markdown := fs.String("markdown", "", "write a Markdown summary, such as $GITHUB_STEP_SUMMARY")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: recoverylab audit [--bursts N] [--format text|json] [--output AUDIT.json] SCENARIO.json")
		return 2
	}
	if *format != "text" && *format != "json" {
		fmt.Fprintln(os.Stderr, "--format must be text or json")
		return 2
	}
	if *bursts < 1 || *bursts > 100 {
		fmt.Fprintln(os.Stderr, "--bursts must be between 1 and 100")
		return 2
	}
	base, err := config.Parse(fs.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid scenario:", err)
		return 2
	}
	steps := audit.Plan(base, *bursts)
	runnable := 0
	for _, step := range steps {
		if step.SkipReason == "" {
			runnable++
		}
	}
	if runnable == 0 {
		fmt.Fprintln(os.Stderr, "invalid scenario: nothing to audit")
		for _, step := range steps {
			fmt.Fprintf(os.Stderr, "  %s: %s\n", step.Title, step.SkipReason)
		}
		return 2
	}

	s := newStyle(os.Stdout)
	text := *format == "text"
	if text {
		printAuditHeader(os.Stdout, s, base)
	}
	outcome := audit.Run(context.Background(), base, steps, func(r audit.Result) {
		if text {
			printAuditLine(os.Stdout, s, r)
		}
	})
	encoded, err := json.MarshalIndent(outcome, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, "encode audit:", err)
		return 2
	}
	if *output != "" {
		if err := os.WriteFile(*output, append(encoded, '\n'), 0644); err != nil {
			fmt.Fprintln(os.Stderr, "write audit:", err)
			return 2
		}
	}
	if err := writeReports(outcome, *junit, *html, *markdown); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if text {
		printAuditSummary(os.Stdout, s, outcome)
	} else {
		fmt.Println(string(encoded))
	}
	switch outcome.Status {
	case "PASS":
		return 0
	case "VIOLATION":
		return 1
	default:
		return 2
	}
}

// writeReports saves the optional JUnit and HTML versions of a result.
func writeReports(r audit.Report, junitPath, htmlPath, markdownPath string) error {
	write := func(path, kind string, render func(io.Writer) error) error {
		if path == "" {
			return nil
		}
		// Append so several runs can share one GitHub job summary file.
		flags := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
		if kind == "Markdown" {
			flags = os.O_CREATE | os.O_WRONLY | os.O_APPEND
		}
		f, err := os.OpenFile(path, flags, 0644)
		if err != nil {
			return fmt.Errorf("write %s report: %w", kind, err)
		}
		if err := render(f); err != nil {
			_ = f.Close()
			return fmt.Errorf("write %s report: %w", kind, err)
		}
		return f.Close()
	}
	if err := write(junitPath, "JUnit", func(w io.Writer) error { return report.JUnit(w, r) }); err != nil {
		return err
	}
	if err := write(htmlPath, "HTML", func(w io.Writer) error { return report.HTML(w, r, currentVersion()) }); err != nil {
		return err
	}
	return write(markdownPath, "Markdown", func(w io.Writer) error { return report.Markdown(w, r) })
}

func printAuditHeader(w io.Writer, s style, c config.Config) {
	fmt.Fprintf(w, "\n %s %s\n", s.bold("RecoveryLab audit"), s.dim("· "+c.Name))
	fmt.Fprintf(w, " %s\n\n", s.dim(c.Request.Method+" "+c.Request.URL))
}

func printAuditLine(w io.Writer, s style, r audit.Result) {
	mark := map[string]string{"PASS": s.green("✓"), "VIOLATION": s.red("✗"), "ERROR": s.amber("!"), "SKIPPED": s.dim("–")}[r.Status]
	status := s.badge(r.Status, 10)
	if r.Status == "SKIPPED" {
		status = s.dim(fmt.Sprintf("%-10s", r.Status))
	}
	detail := r.Reason
	if r.Diagnosis != nil {
		detail = r.Diagnosis.Summary
	}
	if r.Runs > 1 {
		detail = fmt.Sprintf("%d of %d runs held. %s", r.Passed, r.Runs, detail)
	}
	fmt.Fprintf(w, "  %s %s %s %s\n", mark, status, s.bold(fmt.Sprintf("%-22s", r.Title)), wrapText(detail, 50, 38))
	if r.Note != "" && r.Status != "SKIPPED" {
		fmt.Fprintf(w, "%s%s\n", strings.Repeat(" ", 38), s.dim(r.Note))
	}
}

func printAuditSummary(w io.Writer, s style, r audit.Report) {
	ran := r.Held + r.Violations + r.Errors
	fmt.Fprintln(w)
	line := fmt.Sprintf("%d of %d checks held", r.Held, ran)
	switch r.Status {
	case "PASS":
		if r.Skipped == 0 {
			line += ". This endpoint handled every failure RecoveryLab threw at it"
		}
		fmt.Fprintf(w, " %s %s\n", s.badge("PASS", 0), s.bold(line+"."))
	case "VIOLATION":
		fmt.Fprintf(w, " %s %s\n", s.badge("VIOLATION", 0), s.bold(line+"."))
	default:
		fmt.Fprintf(w, " %s %s\n", s.badge("ERROR", 0), s.bold(line+"; some checks couldn't finish."))
	}
	if r.Skipped > 0 {
		fmt.Fprintf(w, " %s\n", s.dim(fmt.Sprintf("%d skipped; see the reasons above.", r.Skipped)))
	}
	for _, result := range r.Results {
		if result.Status == "PASS" || result.Status == "SKIPPED" || result.Diagnosis == nil {
			continue
		}
		fmt.Fprintf(w, "\n %s\n", s.bold(result.Title))
		printDiagnosis(w, s, *result.Diagnosis)
	}
	if r.Status == "PASS" {
		fmt.Fprintf(w, "\n %s\n", s.dim("This covers the request and counters in the scenario, not every path through your service."))
	}
	fmt.Fprintln(w)
}
