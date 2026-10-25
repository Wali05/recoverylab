package report

import (
	"fmt"
	"io"
	"strings"

	"github.com/Wali05/recoverylab/internal/audit"
)

// Markdown writes a short summary for a GitHub job summary or a pull request
// comment: one table row per scenario, and the explanation for anything that
// failed folded underneath.
func Markdown(w io.Writer, r audit.Report) error {
	icon := map[string]string{"PASS": "✅", "VIOLATION": "❌", "ERROR": "⚠️", "SKIPPED": "➖"}
	ran := r.Held + r.Violations + r.Errors
	var b strings.Builder
	fmt.Fprintf(&b, "### %s RecoveryLab: %s\n\n", icon[r.Status], cell(r.Name))
	fmt.Fprintf(&b, "`%s %s` · **%d of %d checks held**", r.Method, r.URL, r.Held, ran)
	if r.Skipped > 0 {
		fmt.Fprintf(&b, " · %d skipped", r.Skipped)
	}
	b.WriteString("\n\n| | Scenario | Result |\n|---|---|---|\n")
	for _, result := range r.Results {
		text := result.Reason
		if result.Diagnosis != nil {
			text = result.Diagnosis.Summary
		}
		if result.Runs > 1 {
			text = fmt.Sprintf("%d of %d runs held. %s", result.Passed, result.Runs, text)
		}
		fmt.Fprintf(&b, "| %s | %s | %s |\n", icon[result.Status], cell(result.Title), cell(text))
	}
	for _, result := range r.Results {
		d := result.Diagnosis
		if d == nil || result.Status == "PASS" || (d.Cause == "" && len(d.Fixes) == 0) {
			continue
		}
		fmt.Fprintf(&b, "\n<details><summary>%s: why and how to fix</summary>\n\n", html(result.Title))
		if d.Cause != "" {
			fmt.Fprintf(&b, "**Likely cause:** %s\n\n", d.Cause)
		}
		for _, fix := range d.Fixes {
			fmt.Fprintf(&b, "- %s\n", fix)
		}
		b.WriteString("\n</details>\n")
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// cell keeps user text from breaking out of a table cell.
func cell(s string) string {
	return strings.NewReplacer("|", `\|`, "\n", " ", "\r", "").Replace(html(s))
}

func html(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}
