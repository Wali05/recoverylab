package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Wali05/recoverylab/internal/audit"
	"github.com/Wali05/recoverylab/internal/config"
	"github.com/Wali05/recoverylab/internal/report"
)

func runAudit(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("audit", flag.ContinueOnError)
	format := fs.String("format", "text", "text or json")
	output := fs.String("output", "", "write the JSON audit to a file")
	bursts := fs.Int("bursts", audit.DefaultOptions.Bursts, "concurrent bursts to try, 1-100; races don't show up every time")
	crashPoints := fs.Int("crash-points", audit.DefaultOptions.CrashPoints, "times to kill a managed service mid-request, 0-32, each at a different moment; 0 skips the check")
	junit := fs.String("junit", "", "write a JUnit XML report for CI")
	html := fs.String("html", "", "write a self-contained HTML report")
	markdown := fs.String("markdown", "", "write a Markdown summary, such as $GITHUB_STEP_SUMMARY")
	badge := fs.String("badge", "", "write an SVG badge for a README")
	if err := fs.Parse(args); err != nil {
		return flagExit(err)
	}
	if fs.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: recoverylab audit [--bursts N] [--crash-points N] [--format text|json] [--output AUDIT.json] SCENARIO.json|DIR ...")
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
	if *crashPoints < 0 || *crashPoints > 32 {
		fmt.Fprintln(os.Stderr, "--crash-points must be between 0 and 32")
		return 2
	}
	files, err := scenarioFiles(fs.Args())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}

	// Check every file before running anything, so a typo in the tenth
	// scenario doesn't waste the time spent on the first nine.
	type planned struct {
		base  config.Config
		steps []audit.Step
	}
	var plans []planned
	for _, file := range files {
		base, err := config.Parse(file)
		if err != nil {
			fmt.Fprintln(os.Stderr, "invalid scenario:", err)
			return 2
		}
		steps := audit.Plan(base, audit.Options{Bursts: *bursts, CrashPoints: *crashPoints})
		runnable := false
		for _, step := range steps {
			runnable = runnable || step.SkipReason == ""
		}
		if !runnable {
			fmt.Fprintf(os.Stderr, "invalid scenario %s: nothing to audit\n", file)
			for _, step := range steps {
				fmt.Fprintf(os.Stderr, "  %s: %s\n", step.Title, step.SkipReason)
			}
			return 2
		}
		plans = append(plans, planned{base, steps})
	}

	s := newStyle(os.Stdout)
	text := *format == "text"
	var outcomes []audit.Report
	for _, p := range plans {
		if text {
			printAuditHeader(os.Stdout, s, p.base)
		}
		hooks := audit.Hooks{}
		if text {
			running := func(step audit.Step, done int) {
				label := "… " + step.Title
				if step.Runs > 1 {
					label += fmt.Sprintf("  %d of %d", min(done+1, step.Runs), step.Runs)
				}
				s.progress(os.Stdout, label)
			}
			hooks.Start = func(step audit.Step) { running(step, 0) }
			hooks.Tick = func(step audit.Step, done int) {
				if done < step.Runs {
					running(step, done)
				}
			}
			hooks.Done = func(r audit.Result) {
				s.clearProgress(os.Stdout)
				printAuditLine(os.Stdout, s, r)
			}
		}
		outcome := audit.RunWith(ctx, p.base, p.steps, hooks)
		if text {
			printAuditSummary(os.Stdout, s, outcome)
		}
		outcomes = append(outcomes, outcome)
		if ctx.Err() != nil {
			fmt.Fprintln(os.Stderr, "Interrupted. Any managed service was stopped.")
			return interrupted
		}
	}
	suite := audit.Combine(outcomes)
	var payload any = suite
	if len(outcomes) == 1 {
		payload = outcomes[0]
	}
	encoded, err := json.MarshalIndent(payload, "", "  ")
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
	if err := writeReports(outcomes, reportPaths{*junit, *html, *markdown, *badge}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if !text {
		fmt.Println(string(encoded))
	} else if len(outcomes) > 1 {
		printSuiteSummary(os.Stdout, s, suite)
	}
	switch suite.Status {
	case "PASS":
		return 0
	case "VIOLATION":
		return 1
	default:
		return 2
	}
}

// scenarioFiles expands folders to the .json files directly inside them,
// in name order.
func scenarioFiles(args []string) ([]string, error) {
	var files []string
	for _, arg := range args {
		info, err := os.Stat(arg)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			files = append(files, arg)
			continue
		}
		matches, err := filepath.Glob(filepath.Join(arg, "*.json"))
		if err != nil {
			return nil, err
		}
		if len(matches) == 0 {
			return nil, fmt.Errorf("%s has no .json scenarios", arg)
		}
		sort.Strings(matches)
		files = append(files, matches...)
	}
	return files, nil
}

func printSuiteSummary(w io.Writer, s style, suite audit.Suite) {
	fmt.Fprintf(w, "%s\n\n %s %s\n\n", s.dim(strings.Repeat("─", 72)), s.badge(suite.Status, 0), s.bold(fmt.Sprintf("%d endpoints · %d of %d checks held", suite.Endpoints, suite.Held, suite.Ran())))
	for _, r := range suite.Audits {
		mark := map[string]string{"PASS": s.green("✓"), "VIOLATION": s.red("✗"), "ERROR": s.amber("!")}[r.Status]
		fmt.Fprintf(w, "  %s %s %s\n", mark, fmt.Sprintf("%-40s", r.Method+" "+r.URL), s.dim(fmt.Sprintf("%d of %d held", r.Held, r.Held+r.Violations+r.Errors)))
	}
	fmt.Fprintln(w)
}

// writeReports saves the optional JUnit, HTML, badge, and Markdown versions
// of a result.
type reportPaths struct{ junit, html, markdown, badge string }

func writeReports(r []audit.Report, paths reportPaths) error {
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
	if err := write(paths.junit, "JUnit", func(w io.Writer) error { return report.JUnit(w, r) }); err != nil {
		return err
	}
	if err := write(paths.html, "HTML", func(w io.Writer) error { return report.HTML(w, r, currentVersion()) }); err != nil {
		return err
	}
	if err := write(paths.badge, "badge", func(w io.Writer) error { return report.Badge(w, r) }); err != nil {
		return err
	}
	return write(paths.markdown, "Markdown", func(w io.Writer) error { return report.Markdown(w, r) })
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
