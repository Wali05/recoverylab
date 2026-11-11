package report

import (
	_ "embed"
	"fmt"
	"html/template"
	"io"
	"time"

	"github.com/Wali05/recoverylab/internal/audit"
	"github.com/Wali05/recoverylab/internal/engine"
)

//go:embed report.html.tmpl
var page string

var tmpl = template.Must(template.New("report").Funcs(template.FuncMap{
	"offset": offset,
	"deref":  func(v *int64) int64 { return *v },
	"lower": func(s string) string {
		return map[string]string{"PASS": "pass", "VIOLATION": "violation", "ERROR": "error", "SKIPPED": "skipped"}[s]
	},
	"mark": func(s string) string {
		return map[string]string{"PASS": "✓", "VIOLATION": "✗", "ERROR": "!", "SKIPPED": "–"}[s]
	},
	"pick": pick,
	"wanted": func(c engine.StateCheck) string {
		if len(c.Allowed) > 0 && c.Baseline != c.Expected {
			return fmt.Sprintf("%d or %d", c.Baseline, c.Expected)
		}
		return fmt.Sprint(c.Expected)
	},
	"ran":   func(r audit.Report) int { return r.Held + r.Violations + r.Errors },
	"tally": tally,
}).Parse(page))

type htmlPage struct {
	Audits    []audit.Report
	Suite     audit.Suite
	Single    bool
	Title     string
	Version   string
	Generated string
}

// HTML writes a self-contained page: no scripts, fonts, or network requests,
// so it can be attached to a CI run or a pull request and opened anywhere.
// Several audits get a summary with links to each endpoint.
func HTML(w io.Writer, audits []audit.Report, version string) error {
	return tmpl.Execute(w, htmlPage{
		Audits:    audits,
		Suite:     audit.Combine(audits),
		Single:    len(audits) == 1,
		Title:     suiteName(audits),
		Version:   version,
		Generated: time.Now().UTC().Format("2 Jan 2006 15:04 UTC"),
	})
}

// pick shows the run that best explains a result, the same one the
// diagnosis was written from.
func pick(r audit.Result) *engine.Report {
	for _, want := range []string{"VIOLATION", "ERROR", "PASS"} {
		for i := range r.Reports {
			if r.Reports[i].Status == want {
				return &r.Reports[i]
			}
		}
	}
	return nil
}

func offset(started, at string) string {
	a, err1 := time.Parse(time.RFC3339Nano, started)
	b, err2 := time.Parse(time.RFC3339Nano, at)
	if err1 != nil || err2 != nil {
		return ""
	}
	return fmt.Sprintf("+%d ms", b.Sub(a).Milliseconds())
}

// tally groups HTTP outcomes, which keeps a 64-request burst to one line.
func tally(attempts []engine.Attempt) []string {
	var order []string
	counts := map[string]int{}
	for _, a := range attempts {
		key := fmt.Sprintf("HTTP %d", a.HTTPStatus)
		if a.ClientError != "" {
			key = "no reply"
		}
		if counts[key] == 0 {
			order = append(order, key)
		}
		counts[key]++
	}
	out := make([]string, len(order))
	for i, key := range order {
		out[i] = fmt.Sprintf("%s × %d", key, counts[key])
		if counts[key] == 1 {
			out[i] = key
		}
	}
	return out
}
