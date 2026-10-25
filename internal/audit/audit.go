// Package audit runs every failure that applies to one request and collects
// the results into a single report card.
package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/Wali05/recoverylab/internal/config"
	"github.com/Wali05/recoverylab/internal/engine"
	"github.com/Wali05/recoverylab/internal/explain"
)

// Step is one scenario in an audit. A step with SkipReason set never runs.
type Step struct {
	Scenario   string
	Title      string
	Runs       int
	Note       string
	SkipReason string
	Config     config.Config
}

type Result struct {
	Scenario   string             `json:"scenario"`
	Title      string             `json:"title"`
	Status     string             `json:"status"`
	Note       string             `json:"note,omitempty"`
	Reason     string             `json:"skip_reason,omitempty"`
	Runs       int                `json:"runs"`
	Passed     int                `json:"passed"`
	Violations int                `json:"violations"`
	Errors     int                `json:"errors"`
	Diagnosis  *explain.Diagnosis `json:"diagnosis,omitempty"`
	Reports    []engine.Report    `json:"reports,omitempty"`
}

type Report struct {
	Name       string   `json:"name"`
	Method     string   `json:"method"`
	URL        string   `json:"url"`
	Status     string   `json:"status"`
	Held       int      `json:"held"`
	Violations int      `json:"violations"`
	Errors     int      `json:"errors"`
	Skipped    int      `json:"skipped"`
	Results    []Result `json:"results"`
}

var titles = map[string]string{
	"lost-response":         "Lost response",
	"concurrent-duplicates": "Concurrent duplicates",
	"key-reuse":             "Key reuse",
	"crash-after-ack":       "Crash after ack",
}

// Plan derives one scenario per failure from a base config. The base can
// name any scenario, or none; settings that belong to a single scenario are
// only carried into that one.
func Plan(base config.Config, bursts int) []Step {
	clean := base
	clean.Concurrency = 0
	clean.ConcurrentResponse = nil
	clean.RetryResponse = nil
	clean.ChangedBody = nil
	clean.ExpectedReuseStatus = 0

	lost := clean
	lost.Scenario = "lost-response"
	lost.RetryResponse = base.RetryResponse

	burst := clean
	burst.Scenario = "concurrent-duplicates"
	burst.Concurrency = base.Concurrency
	burst.ConcurrentResponse = base.ConcurrentResponse

	steps := []Step{
		{Scenario: lost.Scenario, Runs: 1, Config: lost},
		{Scenario: burst.Scenario, Runs: bursts, Config: burst, Note: fmt.Sprintf("%d bursts of %d", bursts, burst.Workers())},
	}

	reuse := clean
	reuse.Scenario = "key-reuse"
	reuse.ExpectedReuseStatus = base.ExpectedReuseStatus
	step := Step{Scenario: reuse.Scenario, Runs: 1}
	switch {
	case !hasKey(base.Request.Headers):
		step.SkipReason = "The request has no Idempotency-Key header, so there's no key to reuse."
	case len(base.ChangedBody) > 0:
		reuse.ChangedBody = base.ChangedBody
		step.Note = "using changed_body"
	default:
		changed, note, err := ChangeBody(base.Request.Body)
		if err != nil {
			step.SkipReason = err.Error()
		}
		reuse.ChangedBody, step.Note = changed, note
	}
	step.Config = reuse
	steps = append(steps, step)

	crash := clean
	crash.Scenario = "crash-after-ack"
	crash.RetryResponse = base.RetryResponse
	if crash.RetryResponse == nil {
		// An audit always retries after restart: it catches both a lost write
		// and a service that forgets its keys when it restarts.
		crash.RetryResponse = &config.RetryResponse{}
	}
	step = Step{Scenario: crash.Scenario, Runs: 1, Config: crash}
	if base.Service == nil {
		step.SkipReason = `Add a "service" block so RecoveryLab can start, kill, and restart your process.`
	}
	steps = append(steps, step)

	for i := range steps {
		steps[i].Title = titles[steps[i].Scenario]
		if steps[i].SkipReason == "" {
			if err := steps[i].Config.Validate(); err != nil {
				steps[i].SkipReason = "Invalid for this scenario: " + err.Error()
			}
		}
	}
	return steps
}

func hasKey(headers map[string]string) bool {
	for k, v := range headers {
		if strings.EqualFold(k, "Idempotency-Key") && strings.TrimSpace(v) != "" {
			return true
		}
	}
	return false
}

// ChangeBody makes the smallest real change to a JSON body so the audit can
// reuse its key for a different request: the first number goes up by one, or
// failing that the first string gets a suffix, or the first boolean flips.
// Strings carrying {{run_id}} are left alone because they often hold the key.
func ChangeBody(body json.RawMessage) (json.RawMessage, string, error) {
	if len(body) == 0 {
		return nil, "", fmt.Errorf("the request has no JSON body to change; add changed_body to test key reuse")
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var root any
	if err := dec.Decode(&root); err != nil {
		return nil, "", err
	}
	for _, kind := range []string{"number", "string", "bool"} {
		if pointer, note, ok := mutate(&root, "", kind); ok {
			changed, err := json.Marshal(root)
			if err != nil {
				return nil, "", err
			}
			return changed, "changed " + displayPointer(pointer) + " " + note, nil
		}
	}
	return nil, "", fmt.Errorf("couldn't find a value to change in the body; add changed_body to test key reuse")
}

func displayPointer(p string) string {
	if p == "" {
		return "the body"
	}
	return p
}

func mutate(node *any, pointer, kind string) (string, string, bool) {
	switch v := (*node).(type) {
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			child := v[k]
			escaped := strings.ReplaceAll(strings.ReplaceAll(k, "~", "~0"), "/", "~1")
			if p, note, ok := mutate(&child, pointer+"/"+escaped, kind); ok {
				v[k] = child
				return p, note, true
			}
		}
	case []any:
		for i := range v {
			if p, note, ok := mutate(&v[i], pointer+"/"+strconv.Itoa(i), kind); ok {
				return p, note, true
			}
		}
	case json.Number:
		if kind != "number" {
			return "", "", false
		}
		if n, err := v.Int64(); err == nil && n < math.MaxInt64 {
			*node = json.Number(strconv.FormatInt(n+1, 10))
		} else if f, err := v.Float64(); err == nil {
			*node = json.Number(strconv.FormatFloat(f+1, 'g', -1, 64))
		} else {
			return "", "", false
		}
		return pointer, fmt.Sprintf("from %s to %s", v, *node), true
	case string:
		if kind != "string" || strings.Contains(v, "{{run_id}}") {
			return "", "", false
		}
		*node = v + "-changed"
		return pointer, fmt.Sprintf("from %q to %q", v, *node), true
	case bool:
		if kind != "bool" {
			return "", "", false
		}
		*node = !v
		return pointer, fmt.Sprintf("from %t to %t", v, !v), true
	}
	return "", "", false
}

// Run executes the plan in order. progress, when set, is called as each
// step finishes so the caller can print results as they arrive.
func Run(ctx context.Context, base config.Config, steps []Step, progress func(Result)) Report {
	report := Report{Name: base.Name, Method: strings.ToUpper(base.Request.Method), URL: base.Request.URL, Status: "PASS"}
	for _, step := range steps {
		result := Result{Scenario: step.Scenario, Title: step.Title, Note: step.Note}
		if step.SkipReason != "" {
			result.Status, result.Reason = "SKIPPED", step.SkipReason
			report.Skipped++
		} else {
			for i := 0; i < step.Runs; i++ {
				r := engine.Run(ctx, step.Config)
				result.Reports = append(result.Reports, r)
				switch r.Status {
				case "PASS":
					result.Passed++
				case "VIOLATION":
					result.Violations++
				default:
					result.Errors++
				}
			}
			result.Runs = len(result.Reports)
			result.Status = "PASS"
			if result.Violations > 0 {
				result.Status = "VIOLATION"
			} else if result.Errors > 0 {
				result.Status = "ERROR"
			}
			d := explain.Report(first(result))
			result.Diagnosis = &d
			switch result.Status {
			case "PASS":
				report.Held++
			case "VIOLATION":
				report.Violations++
			default:
				report.Errors++
			}
		}
		report.Results = append(report.Results, result)
		if progress != nil {
			progress(result)
		}
	}
	if report.Violations > 0 {
		report.Status = "VIOLATION"
	} else if report.Errors > 0 {
		report.Status = "ERROR"
	}
	return report
}

// first returns the run that best represents a result: the first violation,
// then the first error, then any run at all.
func first(r Result) engine.Report {
	for _, want := range []string{"VIOLATION", "ERROR"} {
		for _, report := range r.Reports {
			if report.Status == want {
				return report
			}
		}
	}
	return r.Reports[0]
}
