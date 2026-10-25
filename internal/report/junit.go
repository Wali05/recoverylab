// Package report writes audit results in formats other tools read: JUnit XML
// for CI dashboards and a single-file HTML page for people.
package report

import (
	"encoding/xml"
	"fmt"
	"io"
	"strings"

	"github.com/Wali05/recoverylab/internal/audit"
	"github.com/Wali05/recoverylab/internal/explain"
)

type junitSuites struct {
	XMLName  xml.Name     `xml:"testsuites"`
	Name     string       `xml:"name,attr"`
	Tests    int          `xml:"tests,attr"`
	Failures int          `xml:"failures,attr"`
	Errors   int          `xml:"errors,attr"`
	Skipped  int          `xml:"skipped,attr"`
	Suites   []junitSuite `xml:"testsuite"`
}

type junitSuite struct {
	Name     string      `xml:"name,attr"`
	Tests    int         `xml:"tests,attr"`
	Failures int         `xml:"failures,attr"`
	Errors   int         `xml:"errors,attr"`
	Skipped  int         `xml:"skipped,attr"`
	Time     string      `xml:"time,attr"`
	Cases    []junitCase `xml:"testcase"`
}

type junitCase struct {
	Name      string        `xml:"name,attr"`
	Classname string        `xml:"classname,attr"`
	Time      string        `xml:"time,attr"`
	Failure   *junitProblem `xml:"failure,omitempty"`
	Error     *junitProblem `xml:"error,omitempty"`
	Skipped   *junitProblem `xml:"skipped,omitempty"`
}

type junitProblem struct {
	Message string `xml:"message,attr"`
	Text    string `xml:",chardata"`
}

// JUnit writes one test case per run, grouped into one suite per scenario.
// Each failure carries the plain-language explanation, which is what shows
// up in a CI test tab.
func JUnit(w io.Writer, r audit.Report) error {
	out := junitSuites{Name: "RecoveryLab: " + r.Name}
	for _, result := range r.Results {
		suite := junitSuite{Name: result.Title, Time: "0"}
		if result.Status == "SKIPPED" {
			suite.Cases = []junitCase{{Name: result.Title, Classname: r.Name, Time: "0", Skipped: &junitProblem{Message: result.Reason}}}
			suite.Tests, suite.Skipped = 1, 1
		}
		var total int64
		for i, run := range result.Reports {
			name := result.Title
			if len(result.Reports) > 1 {
				name = fmt.Sprintf("%s (run %d)", result.Title, i+1)
			}
			c := junitCase{Name: name, Classname: r.Name, Time: seconds(run.DurationMS)}
			total += run.DurationMS
			detail := explanation(explain.Report(run), run.Error)
			switch run.Status {
			case "PASS":
			case "VIOLATION":
				c.Failure = &junitProblem{Message: run.Error, Text: detail}
				suite.Failures++
			default:
				c.Error = &junitProblem{Message: run.Error, Text: detail}
				suite.Errors++
			}
			suite.Cases = append(suite.Cases, c)
			suite.Tests++
		}
		if len(result.Reports) > 0 {
			suite.Time = seconds(total)
		}
		out.Tests += suite.Tests
		out.Failures += suite.Failures
		out.Errors += suite.Errors
		out.Skipped += suite.Skipped
		out.Suites = append(out.Suites, suite)
	}
	if _, err := io.WriteString(w, xml.Header); err != nil {
		return err
	}
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	if err := enc.Encode(out); err != nil {
		return err
	}
	_, err := io.WriteString(w, "\n")
	return err
}

func explanation(d explain.Diagnosis, raw string) string {
	lines := []string{d.Summary}
	if d.Cause != "" {
		lines = append(lines, "", "Likely cause: "+d.Cause)
	}
	for i, fix := range d.Fixes {
		if i == 0 {
			lines = append(lines, "", "How to fix:")
		}
		lines = append(lines, "  - "+fix)
	}
	if raw != "" {
		lines = append(lines, "", raw)
	}
	return strings.Join(lines, "\n")
}

func seconds(ms int64) string { return fmt.Sprintf("%.3f", float64(ms)/1000) }
