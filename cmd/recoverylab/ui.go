package main

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"

	"github.com/Wali05/recoverylab/internal/engine"
	"github.com/Wali05/recoverylab/internal/explain"
)

// style adds ANSI color only when stdout is an interactive terminal. NO_COLOR
// (https://no-color.org) always wins; FORCE_COLOR is handy for recordings.
type style struct{ on bool }

func newStyle(f *os.File) style {
	if os.Getenv("NO_COLOR") != "" {
		return style{}
	}
	if os.Getenv("FORCE_COLOR") != "" {
		return style{on: true}
	}
	info, err := f.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 || os.Getenv("TERM") == "dumb" {
		return style{}
	}
	// Old Windows consoles print escape codes literally. Windows Terminal and
	// VS Code set these variables and handle them fine.
	if runtime.GOOS == "windows" && os.Getenv("WT_SESSION") == "" && os.Getenv("TERM_PROGRAM") == "" {
		return style{}
	}
	return style{on: true}
}

func (s style) wrap(code, text string) string {
	if !s.on {
		return text
	}
	return "\x1b[" + code + "m" + text + "\x1b[0m"
}

func (s style) bold(t string) string  { return s.wrap("1", t) }
func (s style) dim(t string) string   { return s.wrap("2", t) }
func (s style) green(t string) string { return s.wrap("32", t) }
func (s style) red(t string) string   { return s.wrap("31", t) }
func (s style) amber(t string) string { return s.wrap("33", t) }

// badge pads before coloring so columns still line up with color on.
func (s style) badge(status string, width int) string {
	padded := fmt.Sprintf("%-*s", width, status)
	switch status {
	case "PASS":
		return s.wrap("1;32", padded)
	case "VIOLATION":
		return s.wrap("1;31", padded)
	default:
		return s.wrap("1;33", padded)
	}
}

// wrapText breaks text at spaces so it fits width columns, indenting every
// line after the first by indent spaces.
func wrapText(text string, width, indent int) string {
	words := strings.Fields(text)
	if len(words) == 0 {
		return ""
	}
	var b strings.Builder
	line := 0
	for i, w := range words {
		if i > 0 && line+1+len(w) > width {
			b.WriteString("\n" + strings.Repeat(" ", indent))
			line = 0
		} else if i > 0 {
			b.WriteByte(' ')
			line++
		}
		b.WriteString(w)
		line += len(w)
	}
	return b.String()
}

func printDiagnosis(w io.Writer, s style, d explain.Diagnosis) {
	const label, width = 15, 62
	fmt.Fprintf(w, "  %s%s\n", s.bold(fmt.Sprintf("%-*s", label, "What happened")), wrapText(d.Summary, width, label+2))
	if d.Cause != "" {
		fmt.Fprintf(w, "  %s%s\n", s.bold(fmt.Sprintf("%-*s", label, "Likely cause")), wrapText(d.Cause, width, label+2))
	}
	for i, fix := range d.Fixes {
		head := ""
		if i == 0 {
			head = "How to fix"
		}
		fmt.Fprintf(w, "  %s• %s\n", s.bold(fmt.Sprintf("%-*s", label, head)), wrapText(fix, width-2, label+4))
	}
}

func printReport(w io.Writer, s style, r engine.Report) {
	fmt.Fprintf(w, "\n %s %s\n", s.badge(r.Status, 0), s.bold(r.Name))
	meta := []string{r.Scenario}
	if r.RunID != "" {
		meta = append(meta, "run "+r.RunID)
	}
	meta = append(meta, fmt.Sprintf("%d ms", r.DurationMS))
	fmt.Fprintln(w, " "+s.dim(strings.Join(meta, " · ")))
	fmt.Fprintln(w)
	for _, e := range r.Events {
		fmt.Fprintf(w, "  %s %s\n", s.dim(fmt.Sprintf("%-14s", e.Step)), e.Detail)
	}
	if len(r.Events) > 0 {
		fmt.Fprintln(w)
	}
	printChecks(w, s, "after restart", r.AfterRestartChecks)
	printChecks(w, s, "", r.Checks)
	if r.Status == "PASS" {
		fmt.Fprintf(w, "  %s\n\n", s.green(explain.Report(r).Summary))
		return
	}
	printDiagnosis(w, s, explain.Report(r))
	if r.Error != "" {
		fmt.Fprintf(w, "\n  %s\n", s.dim(wrapText(r.Error, 76, 2)))
	}
	fmt.Fprintln(w)
}

func printChecks(w io.Writer, s style, suffix string, checks []engine.StateCheck) {
	printed := false
	for _, c := range checks {
		if c.Observed == nil {
			continue
		}
		name := c.Name
		if suffix != "" {
			name += " (" + suffix + ")"
		}
		mark, value := s.green("✓"), fmt.Sprint(*c.Observed)
		if *c.Observed != c.Expected {
			mark, value = s.red("✗"), s.red(value)
		}
		fmt.Fprintf(w, "  %s %-26s %d → %s  %s\n", mark, name, c.Baseline, value, s.dim(fmt.Sprintf("expected %d", c.Expected)))
		printed = true
	}
	if printed {
		fmt.Fprintln(w)
	}
}

func printBatch(w io.Writer, s style, b BatchReport) {
	fmt.Fprintf(w, "\n %s %s\n", s.badge(b.Status, 0), s.bold(fmt.Sprintf("%d runs", b.Runs)))
	fmt.Fprintln(w)
	for i, r := range b.Reports {
		fmt.Fprintf(w, "  %3d  %s %s", i+1, s.badge(r.Status, 10), s.dim("run="+r.RunID))
		if r.Error != "" {
			fmt.Fprintf(w, "  %s", r.Error)
		}
		fmt.Fprintln(w)
	}
	fmt.Fprintf(w, "\n  Passed: %d  Violations: %d  Errors: %d\n", b.Passed, b.Violations, b.Errors)
	// A batch usually fails the same way every time, so one explanation of
	// the first failure is more useful than one per run.
	for _, r := range b.Reports {
		if r.Status != "PASS" {
			fmt.Fprintln(w)
			printDiagnosis(w, s, explain.Report(r))
			break
		}
	}
	fmt.Fprintln(w)
}
