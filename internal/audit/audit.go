// Package audit runs every failure that applies to one request and collects
// the results into a single report card.
package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

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
	// Sweep makes each run differ: it returns the config for run i, given
	// how long the service took to answer earlier in this audit (zero when
	// nothing measured it) and this step's runs so far.
	Sweep func(i int, typical time.Duration, prior []engine.Report) config.Config
}

// Options sets how hard an audit tries to catch failures that don't show up
// every time.
type Options struct {
	// Bursts is how many concurrent bursts to send.
	Bursts int
	// CrashPoints is how many times to kill the service mid-request, each
	// at a different moment. Zero leaves the check out.
	CrashPoints int
}

// DefaultOptions is what "recoverylab audit" uses without flags.
var DefaultOptions = Options{Bursts: 5, CrashPoints: 6}

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
	"crash-mid-request":     "Crash mid-request",
}

// Plan derives one scenario per failure from a base config. The base can
// name any scenario, or none; settings that belong to a single scenario are
// only carried into that one.
func Plan(base config.Config, opts Options) []Step {
	bursts := opts.Bursts
	clean := base
	clean.Concurrency = 0
	clean.ConcurrentResponse = nil
	clean.RetryResponse = nil
	clean.ChangedBody = nil
	clean.ExpectedReuseStatus = 0
	clean.CrashDelay = ""

	lost := clean
	lost.Scenario = "lost-response"
	lost.RetryResponse = withIDs(base.RetryResponse)

	burst := clean
	burst.Scenario = "concurrent-duplicates"
	burst.Concurrency = base.Concurrency
	burst.ConcurrentResponse = base.ConcurrentResponse
	if burst.ConcurrentResponse == nil {
		// A duplicate that arrives mid-request should get a 409, as in the
		// IETF Idempotency-Key draft and Stripe's API, not a server error.
		burst.ConcurrentResponse = &config.ConcurrentResponse{NoServerErrors: true}
	}

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
	// An audit always retries after restart: it catches both a lost write
	// and a service that forgets its keys when it restarts.
	crash.RetryResponse = withIDs(base.RetryResponse)
	step = Step{Scenario: crash.Scenario, Runs: 1, Config: crash}
	if base.Service == nil {
		step.SkipReason = `Add a "service" block so RecoveryLab can start, kill, and restart your process.`
	}
	steps = append(steps, step)

	// Kill the service while it is still handling the write, at several
	// different moments, because the dangerous window is usually narrow.
	mid := clean
	mid.Scenario = "crash-mid-request"
	mid.RetryResponse = withIDs(base.RetryResponse)
	step = Step{Scenario: mid.Scenario, Runs: opts.CrashPoints, Config: mid}
	step.Sweep = func(i int, typical time.Duration, prior []engine.Report) config.Config {
		c := mid
		c.CrashDelay = crashDelay(i, opts.CrashPoints, replyTime(typical, prior)).String()
		return c
	}
	switch {
	case base.Service == nil:
		step.SkipReason = `Add a "service" block so RecoveryLab can start, kill, and restart your process.`
	case opts.CrashPoints <= 0:
		step.SkipReason = "Turned off with --crash-points 0."
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

// crashDelay spreads n kill points across a request. When something has
// measured how long the service takes to answer, the points sit evenly
// between "the request just arrived" and that time, which is where a
// half-finished write can be caught. Without a measurement they double from
// zero: 0, 1 ms, 2 ms, 4 ms, and so on.
func crashDelay(i, n int, reply time.Duration) time.Duration {
	if reply <= 0 {
		if i == 0 {
			return 0
		}
		return time.Millisecond << min(i-1, 14)
	}
	d := time.Duration(float64(reply) * float64(i) / float64(max(n, 1)))
	return min(d, 30*time.Second).Round(time.Microsecond)
}

// replyTime narrows the estimate as the sweep goes: a kill that arrived
// after the reply shows how quickly this service can answer, and a later
// kill has to come sooner than that to land mid-request.
func replyTime(typical time.Duration, prior []engine.Report) time.Duration {
	for _, r := range prior {
		if r.CrashPoint == nil || !r.CrashPoint.ReplyReceived {
			continue
		}
		seen := time.Duration(r.CrashPoint.DelayMS * float64(time.Millisecond))
		if seen > 0 && (typical <= 0 || seen < typical) {
			typical = seen
		}
	}
	return typical
}

// withIDs asks a retry to return the same IDs as the original reply, unless
// the scenario already names the values to compare. A retry that creates
// nothing new but hands back a different ID still leaves the client with two
// IDs for one thing.
func withIDs(rule *config.RetryResponse) *config.RetryResponse {
	out := config.RetryResponse{CompareIDs: true}
	if rule != nil {
		out = *rule
		out.CompareIDs = out.CompareIDs || len(out.SameJSONPointers) == 0
	}
	return &out
}

func hasKey(headers map[string]string) bool {
	_, ok := config.IdempotencyKey(headers)
	return ok
}

// ChangeBody makes the smallest real change to a JSON body so the audit can
// reuse its key for a different request: the first number goes up by one, or
// failing that the first string changes, or the first boolean flips. Strings
// carrying {{run_id}} or {{uuid}} are changed only as a last resort, because
// they may be how the service identifies the operation.
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
	for _, kind := range []string{"number", "string", "bool", "run-string"} {
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
		templated := strings.Contains(v, "{{run_id}}") || strings.Contains(v, "{{uuid}}")
		if (kind == "string" && templated) || (kind == "run-string" && !templated) || (kind != "string" && kind != "run-string") {
			return "", "", false
		}
		// Prefix anything that looks like an email: a suffix would make it
		// invalid, and a validation 422 would pass for a key-reuse rejection.
		if strings.Contains(v, "@") {
			*node = "changed-" + v
		} else {
			*node = v + "-changed"
		}
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
	return RunWith(ctx, base, steps, Hooks{Done: progress})
}

// Hooks lets a caller follow an audit while it runs. Any of them may be nil.
type Hooks struct {
	// Start is called before a step's first run. Skipped steps don't start.
	Start func(step Step)
	// Tick is called after each run with how many are done.
	Tick func(step Step, done int)
	// Done is called with each step's result, skipped ones included.
	Done func(Result)
}

// RunWith is Run with hooks for live progress.
func RunWith(ctx context.Context, base config.Config, steps []Step, hooks Hooks) Report {
	report := Report{Name: base.Name, Method: strings.ToUpper(base.Request.Method), URL: base.Request.URL, Status: "PASS"}
	var waits []time.Duration
	for _, step := range steps {
		result := Result{Scenario: step.Scenario, Title: step.Title, Note: step.Note}
		if step.SkipReason != "" {
			result.Status, result.Reason = "SKIPPED", step.SkipReason
			report.Skipped++
		} else {
			if hooks.Start != nil {
				hooks.Start(step)
			}
			for i := 0; i < step.Runs && (i == 0 || ctx.Err() == nil); i++ {
				c := step.Config
				if step.Sweep != nil {
					c = step.Sweep(i, median(waits), result.Reports)
				}
				r := engine.Run(ctx, c)
				result.Reports = append(result.Reports, r)
				if wait := serverTime(r); wait > 0 {
					waits = append(waits, wait)
				}
				switch r.Status {
				case "PASS":
					result.Passed++
				case "VIOLATION":
					result.Violations++
				default:
					result.Errors++
				}
				if hooks.Tick != nil {
					hooks.Tick(step, i+1)
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
			if step.Scenario == "crash-mid-request" {
				result.Note = crashNote(result.Reports)
				if result.Status == "PASS" {
					d = explain.CrashSweep(result.Reports)
				}
			}
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
		if hooks.Done != nil {
			hooks.Done(result)
		}
	}
	if report.Violations > 0 {
		report.Status = "VIOLATION"
	} else if report.Errors > 0 {
		report.Status = "ERROR"
	}
	return report
}

// serverTime is how long the service took to answer the write in a run, or
// zero if the run didn't measure it.
func serverTime(r engine.Report) time.Duration {
	for _, a := range r.Attempts {
		if a.Kind == "original" && a.ClientError == "" && a.HTTPStatus >= 200 && a.HTTPStatus < 300 && a.ServerMS > 0 {
			return time.Duration(a.ServerMS * float64(time.Millisecond))
		}
	}
	return 0
}

func median(values []time.Duration) time.Duration {
	if len(values) == 0 {
		return 0
	}
	sorted := slices.Clone(values)
	slices.Sort(sorted)
	return sorted[len(sorted)/2]
}

// crashNote says where the kills landed, so a pass can be read for what it
// is: "6 kills" means little if every one of them came after the reply.
func crashNote(reports []engine.Report) string {
	kills, before := 0, 0
	lo, hi := math.Inf(1), 0.0
	for _, r := range reports {
		if r.CrashPoint == nil {
			continue
		}
		kills++
		if !r.CrashPoint.ReplyReceived {
			before++
		}
		lo, hi = math.Min(lo, r.CrashPoint.DelayMS), math.Max(hi, r.CrashPoint.DelayMS)
	}
	if kills == 0 {
		return ""
	}
	when := fmt.Sprintf("%s to %s ms in", ms(lo), ms(hi))
	if kills == 1 {
		when = ms(lo) + " ms in"
	}
	return fmt.Sprintf("%d %s, %s; %d landed before the reply", kills, plural(kills, "kill", "kills"), when, before)
}

func ms(v float64) string {
	if v < 10 {
		return strconv.FormatFloat(math.Round(v*10)/10, 'f', -1, 64)
	}
	return strconv.FormatFloat(math.Round(v), 'f', -1, 64)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// FromRuns wraps repeated runs of one scenario in an audit report, so every
// output format only has to understand one shape.
func FromRuns(c config.Config, reports []engine.Report) Report {
	result := Result{Scenario: c.Scenario, Title: titles[c.Scenario], Reports: reports, Runs: len(reports)}
	for _, r := range reports {
		switch r.Status {
		case "PASS":
			result.Passed++
		case "VIOLATION":
			result.Violations++
		default:
			result.Errors++
		}
	}
	report := Report{Name: c.Name, Method: strings.ToUpper(c.Request.Method), URL: c.Request.URL, Status: "PASS"}
	switch {
	case result.Violations > 0:
		result.Status, report.Status, report.Violations = "VIOLATION", "VIOLATION", 1
	case result.Errors > 0:
		result.Status, report.Status, report.Errors = "ERROR", "ERROR", 1
	default:
		result.Status, report.Held = "PASS", 1
	}
	d := explain.Report(first(result))
	result.Diagnosis = &d
	report.Results = []Result{result}
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

// Suite is several audits run together, usually one per endpoint.
type Suite struct {
	Status     string   `json:"status"`
	Endpoints  int      `json:"endpoints"`
	Held       int      `json:"held"`
	Violations int      `json:"violations"`
	Errors     int      `json:"errors"`
	Skipped    int      `json:"skipped"`
	Audits     []Report `json:"audits"`
}

func Combine(reports []Report) Suite {
	s := Suite{Status: "PASS", Endpoints: len(reports), Audits: reports}
	for _, r := range reports {
		s.Held += r.Held
		s.Violations += r.Violations
		s.Errors += r.Errors
		s.Skipped += r.Skipped
	}
	if s.Violations > 0 {
		s.Status = "VIOLATION"
	} else if s.Errors > 0 {
		s.Status = "ERROR"
	}
	return s
}

// Ran counts the checks that produced a verdict either way.
func (s Suite) Ran() int { return s.Held + s.Violations + s.Errors }
