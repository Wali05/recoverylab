package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptrace"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wali05/recoverylab/internal/config"
	"github.com/Wali05/recoverylab/internal/strictjson"
)

type Event struct {
	At     string `json:"at"`
	Step   string `json:"step"`
	Detail string `json:"detail"`
}

type Attempt struct {
	Kind        string `json:"kind"`
	HTTPStatus  int    `json:"http_status,omitempty"`
	ClientError string `json:"client_error,omitempty"`
	// DurationMS is how long the request took from the client's side.
	DurationMS float64 `json:"duration_ms,omitempty"`
	// ServerMS is the part of that spent waiting for the service: from the
	// request being written to the first byte of the reply.
	ServerMS float64 `json:"server_ms,omitempty"`
}

func newAttempt(kind string, status int, err error, start time.Time) Attempt {
	return Attempt{Kind: kind, HTTPStatus: status, ClientError: errorString(err), DurationMS: millis(time.Since(start))}
}

func millis(d time.Duration) float64 { return math.Round(d.Seconds()*1e5) / 100 }

type StateCheck struct {
	Name     string `json:"name"`
	Baseline int64  `json:"baseline"`
	Expected int64  `json:"expected"`
	Observed *int64 `json:"observed,omitempty"`
	// Measure is "length" when the value is a list's item count.
	Measure string `json:"measure,omitempty"`
	// Allowed lists every acceptable value when there is more than one. After
	// a crash mid-request, a write may have happened or not; both are fine.
	Allowed []int64 `json:"allowed,omitempty"`
}

// Held reports whether the observed value is acceptable. A check that was
// never observed has nothing against it.
func (c StateCheck) Held() bool {
	if c.Observed == nil || *c.Observed == c.Expected {
		return true
	}
	for _, v := range c.Allowed {
		if *c.Observed == v {
			return true
		}
	}
	return false
}

type ResponseCheck struct {
	OriginalHTTPStatus int      `json:"original_http_status"`
	RetryHTTPStatus    int      `json:"retry_http_status"`
	ExpectedStatus     string   `json:"expected_status"`
	SameJSONPointers   []string `json:"same_json_pointers,omitempty"`
	AutoPointers       bool     `json:"auto_pointers,omitempty"`
	Passed             bool     `json:"passed"`
	Issues             []string `json:"issues,omitempty"`
}

type ConcurrentResponseCheck struct {
	AllowedStatuses []int    `json:"allowed_statuses,omitempty"`
	NoServerErrors  bool     `json:"no_server_errors,omitempty"`
	Passed          bool     `json:"passed"`
	Issues          []string `json:"issues,omitempty"`
}

// CrashPointCheck describes a crash in the middle of a request: when the
// service was killed, and what state the write was left in.
type CrashPointCheck struct {
	// DelayMS is how long after the request was sent the kill was issued.
	DelayMS float64 `json:"delay_ms"`
	// ReplyReceived is true when the service answered before it died, which
	// makes the write acknowledged: it must have survived.
	ReplyReceived bool `json:"reply_received"`
	// Outcome is what the restarted service showed before any retry:
	// "applied", "not-applied", "partial" (some effects but not others), or
	// "unexpected" (a value that is neither before nor after).
	Outcome string   `json:"outcome"`
	Passed  bool     `json:"passed"`
	Issues  []string `json:"issues,omitempty"`
}

type KeyReuseCheck struct {
	ExpectedHTTPStatus int      `json:"expected_http_status"`
	ActualHTTPStatus   int      `json:"actual_http_status"`
	Passed             bool     `json:"passed"`
	Issues             []string `json:"issues,omitempty"`
}

type Report struct {
	Name               string                   `json:"name"`
	Scenario           string                   `json:"scenario"`
	RunID              string                   `json:"run_id"`
	Status             string                   `json:"status"`
	StartedAt          string                   `json:"started_at"`
	DurationMS         int64                    `json:"duration_ms"`
	Baseline           *int64                   `json:"baseline,omitempty"`
	Expected           *int64                   `json:"expected,omitempty"`
	Observed           *int64                   `json:"observed,omitempty"`
	Checks             []StateCheck             `json:"checks,omitempty"`
	AfterRestartChecks []StateCheck             `json:"after_restart_checks,omitempty"`
	RetryResponse      *ResponseCheck           `json:"retry_response,omitempty"`
	ConcurrentResponse *ConcurrentResponseCheck `json:"concurrent_response,omitempty"`
	KeyReuseResponse   *KeyReuseCheck           `json:"key_reuse_response,omitempty"`
	CrashPoint         *CrashPointCheck         `json:"crash_point,omitempty"`
	FaultInjected      bool                     `json:"fault_injected"`
	Attempts           []Attempt                `json:"attempts"`
	Events             []Event                  `json:"events"`
	Error              string                   `json:"error,omitempty"`
}

func (r *Report) event(step, detail string) {
	r.Events = append(r.Events, Event{At: time.Now().UTC().Format(time.RFC3339Nano), Step: step, Detail: detail})
}

func runKeyReuse(ctx context.Context, request config.Request, changedBody json.RawMessage, expectedStatus int, r *Report) error {
	start := time.Now()
	firstStatus, _, firstWait, firstErr := sendTimed(ctx, request, request.URL, false)
	first := newAttempt("original", firstStatus, firstErr, start)
	first.ServerMS = millis(firstWait)
	r.Attempts = append(r.Attempts, first)
	if firstErr != nil {
		return fmt.Errorf("original request: %w", firstErr)
	}
	if firstStatus < 200 || firstStatus >= 300 {
		return fmt.Errorf("service did not acknowledge original operation: HTTP %d", firstStatus)
	}
	r.event("write", fmt.Sprintf("original payload was acknowledged with HTTP %d", firstStatus))

	changedRequest := request
	changedRequest.Body = changedBody
	r.event("key reuse", "sent a changed JSON body with the same method, URL, and Idempotency-Key")
	start = time.Now()
	changedStatus, changedErr := send(ctx, changedRequest, changedRequest.URL)
	r.Attempts = append(r.Attempts, newAttempt("changed-payload", changedStatus, changedErr, start))
	if changedErr != nil {
		return fmt.Errorf("changed-payload request: %w", changedErr)
	}
	check := KeyReuseCheck{ExpectedHTTPStatus: expectedStatus, ActualHTTPStatus: changedStatus, Passed: changedStatus == expectedStatus}
	if !check.Passed {
		check.Issues = append(check.Issues, fmt.Sprintf("changed payload returned HTTP %d; expected HTTP %d", changedStatus, expectedStatus))
		r.event("reuse check", check.Issues[0])
	} else {
		r.event("reuse check", fmt.Sprintf("changed payload was rejected with HTTP %d", changedStatus))
	}
	r.KeyReuseResponse = &check
	return nil
}

// Run performs one experiment. ERROR means the experiment was inconclusive;
// VIOLATION means a state or retry-response check failed.
func Run(parent context.Context, c config.Config) (r Report) {
	start := time.Now()
	r = Report{Name: c.Name, Scenario: c.Scenario, Status: "ERROR", StartedAt: start.UTC().Format(time.RFC3339Nano), Attempts: []Attempt{}, Events: []Event{}}
	defer func() { r.DurationMS = time.Since(start).Milliseconds() }()
	if err := c.Validate(); err != nil {
		r.Error = err.Error()
		return r
	}
	id, err := config.NewRunID()
	if err != nil {
		r.Error = err.Error()
		return r
	}
	r.RunID = id
	if err := c.Resolve(id); err != nil {
		r.Error = err.Error()
		return r
	}
	if err := c.Validate(); err != nil {
		r.Error = err.Error()
		return r
	}
	r.Name = c.Name
	ctx, cancel := context.WithTimeout(parent, c.Duration())
	defer cancel()

	var managed *process
	if c.Service != nil {
		managed = newProcess(*c.Service, c.SourceDir)
		if err := managed.Start(ctx); err != nil {
			r.Error = fmt.Sprintf("start service: %v", err)
			return r
		}
		defer managed.Stop()
		r.event("service", "started and ready")
	}

	observations := c.Observations()
	for _, o := range observations {
		label := o.Name
		if label == "" {
			label = "state"
		}
		baseline, err := observe(ctx, o)
		if err != nil {
			r.Error = fmt.Sprintf("baseline observation %q: %v", label, err)
			return r
		}
		if (*o.ExpectedDelta > 0 && baseline > math.MaxInt64-*o.ExpectedDelta) ||
			(*o.ExpectedDelta < 0 && baseline < math.MinInt64-*o.ExpectedDelta) {
			r.Error = fmt.Sprintf("expected final value overflows int64 for %q", label)
			return r
		}
		expected := baseline + *o.ExpectedDelta
		check := StateCheck{Name: label, Baseline: baseline, Expected: expected}
		if o.Counts() == config.MeasureLength {
			check.Measure = config.MeasureLength
		}
		r.Checks = append(r.Checks, check)
		if len(observations) == 1 {
			r.Baseline = &baseline
			r.Expected = &expected
		}
		r.event("baseline", fmt.Sprintf("%s: observed %d; expected final value %d", label, baseline, expected))
	}

	switch c.Scenario {
	case "lost-response":
		err = runLostResponse(ctx, c.Request, c.RetryResponse, &r)
	case "concurrent-duplicates":
		err = runConcurrent(ctx, c.Request, c.Workers(), c.ConcurrentResponse, &r)
	case "crash-after-ack":
		err = runCrashAfterAck(ctx, c.Request, c.RetryResponse, managed, observations, &r)
	case "crash-mid-request":
		err = runCrashMidRequest(ctx, c.Request, c.RetryResponse, managed, observations, c.CrashAfter(), &r)
	case "key-reuse":
		err = runKeyReuse(ctx, c.Request, c.ChangedBody, c.ReuseStatus(), &r)
	}
	var mismatches []string
	for _, check := range r.AfterRestartChecks {
		if !check.Held() {
			mismatches = append(mismatches, fmt.Sprintf("%s after restart expected %s, observed %d", check.Name, wanted(check), *check.Observed))
		}
	}
	if r.CrashPoint != nil {
		mismatches = append(mismatches, r.CrashPoint.Issues...)
	}
	if err != nil {
		if len(mismatches) > 0 {
			r.Status = "VIOLATION"
			r.Error = "check failed: " + strings.Join(mismatches, "; ") + "; retry incomplete: " + err.Error()
		} else {
			r.Error = err.Error()
		}
		return r
	}
	if r.RetryResponse != nil {
		mismatches = append(mismatches, r.RetryResponse.Issues...)
	}
	if r.ConcurrentResponse != nil {
		mismatches = append(mismatches, r.ConcurrentResponse.Issues...)
	}
	if r.KeyReuseResponse != nil {
		mismatches = append(mismatches, r.KeyReuseResponse.Issues...)
	}

	for i, o := range observations {
		final, err := observe(ctx, o)
		if err != nil {
			if len(mismatches) > 0 {
				r.Status = "VIOLATION"
				r.Error = "check failed: " + strings.Join(mismatches, "; ") + fmt.Sprintf("; final observation %q incomplete: %v", r.Checks[i].Name, err)
			} else {
				r.Error = fmt.Sprintf("final observation %q: %v", r.Checks[i].Name, err)
			}
			return r
		}
		r.Checks[i].Observed = &final
		if len(observations) == 1 {
			r.Observed = &final
		}
		r.event("verify", fmt.Sprintf("%s: expected %d, observed %d", r.Checks[i].Name, r.Checks[i].Expected, final))
		if final != r.Checks[i].Expected {
			mismatches = append(mismatches, fmt.Sprintf("%s expected %d, observed %d", r.Checks[i].Name, r.Checks[i].Expected, final))
		}
	}
	if len(mismatches) > 0 {
		r.Status = "VIOLATION"
		r.Error = "check failed: " + strings.Join(mismatches, "; ")
	} else {
		r.Status = "PASS"
	}
	return r
}

func runConcurrent(ctx context.Context, request config.Request, workers int, responseRule *config.ConcurrentResponse, r *Report) error {
	r.event("fault", fmt.Sprintf("releasing %d copies of the same operation at once", workers))
	start := make(chan struct{})
	results := make([]Attempt, workers)
	var group sync.WaitGroup
	for i := 0; i < workers; i++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			<-start
			sent := time.Now()
			status, err := send(ctx, request, request.URL)
			results[index] = newAttempt(fmt.Sprintf("copy-%02d", index+1), status, err, sent)
		}(i)
	}
	close(start)
	group.Wait()
	r.FaultInjected = true
	r.Attempts = append(r.Attempts, results...)
	statuses := map[int]int{}
	for _, result := range results {
		if result.ClientError != "" {
			return fmt.Errorf("concurrent request failed: %s", result.ClientError)
		}
		statuses[result.HTTPStatus]++
	}
	codes := make([]int, 0, len(statuses))
	for code := range statuses {
		codes = append(codes, code)
	}
	sort.Ints(codes)
	parts := make([]string, 0, len(codes))
	for _, code := range codes {
		parts = append(parts, fmt.Sprintf("%d=%d", code, statuses[code]))
	}
	r.event("responses", fmt.Sprintf("all %d copies completed; HTTP status counts: %s", workers, strings.Join(parts, ", ")))
	if responseRule != nil {
		allowed := make(map[int]bool, len(responseRule.AllowedStatuses))
		for _, status := range responseRule.AllowedStatuses {
			allowed[status] = true
		}
		check := ConcurrentResponseCheck{AllowedStatuses: append([]int(nil), responseRule.AllowedStatuses...), NoServerErrors: responseRule.NoServerErrors, Passed: true}
		for _, code := range codes {
			declared := len(allowed) > 0 && !allowed[code]
			serverError := responseRule.NoServerErrors && code >= 500
			if declared || serverError {
				check.Issues = append(check.Issues, fmt.Sprintf("HTTP %d returned by %d concurrent copies", code, statuses[code]))
			}
		}
		check.Passed = len(check.Issues) == 0
		r.ConcurrentResponse = &check
		if check.Passed {
			r.event("response check", "all concurrent responses used acceptable HTTP statuses")
		} else {
			r.event("response check", strings.Join(check.Issues, "; "))
		}
	}
	return nil
}

func runLostResponse(ctx context.Context, request config.Request, responseRule *config.RetryResponse, r *Report) error {
	captureResponse := capturesReply(responseRule)
	proxy, err := newDropProxy(ctx, request, captureResponse)
	if err != nil {
		return fmt.Errorf("create fault proxy: %w", err)
	}
	defer proxy.Close()
	r.event("fault", "proxy will forward the operation, read the upstream response, then close the client connection without delivering it")
	start := time.Now()
	status, clientErr := send(ctx, request, proxy.URL())
	r.Attempts = append(r.Attempts, newAttempt("original", status, clientErr, start))
	var original proxyResult
	select {
	case result := <-proxy.result:
		if result.err != nil {
			return fmt.Errorf("forward original request: %w", result.err)
		}
		if result.status < 200 || result.status >= 300 {
			return fmt.Errorf("upstream did not acknowledge original operation: HTTP %d", result.status)
		}
		if clientErr == nil {
			return errors.New("fault failed: client received a response")
		}
		r.FaultInjected = true
		r.event("fault", fmt.Sprintf("upstream returned HTTP %d; client saw a lost response", result.status))
		original = result
	case <-ctx.Done():
		return fmt.Errorf("wait for fault proxy: %w", ctx.Err())
	}
	start = time.Now()
	retryStatus, retryBody, retryErr := sendWithBody(ctx, request, request.URL, captureResponse)
	r.Attempts = append(r.Attempts, newAttempt("retry", retryStatus, retryErr, start))
	if retryErr != nil {
		return fmt.Errorf("retry request: %w", retryErr)
	}
	r.event("retry", fmt.Sprintf("resent the same method, URL, body, and configured headers; HTTP %d", retryStatus))
	check, err := checkRetryResponse(responseRule, original.status, original.body, retryStatus, retryBody)
	if err != nil {
		return fmt.Errorf("compare retry response: %w", err)
	}
	r.RetryResponse = &check
	if check.Passed {
		r.event("retry check", "retry response met the declared checks")
	} else {
		r.event("retry check", strings.Join(check.Issues, "; "))
	}
	return nil
}

func runCrashAfterAck(ctx context.Context, request config.Request, responseRule *config.RetryResponse, managed *process, observations []config.Observation, r *Report) error {
	captureResponse := capturesReply(responseRule)
	start := time.Now()
	status, originalBody, wait, err := sendTimed(ctx, request, request.URL, captureResponse)
	original := newAttempt("original", status, err, start)
	original.ServerMS = millis(wait)
	r.Attempts = append(r.Attempts, original)
	if err != nil {
		return fmt.Errorf("original request: %w", err)
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("service did not acknowledge operation: HTTP %d", status)
	}
	r.event("ack", fmt.Sprintf("service acknowledged operation with HTTP %d", status))
	if err := managed.Crash(); err != nil {
		return fmt.Errorf("crash service: %w", err)
	}
	r.FaultInjected = true
	r.event("fault", "killed the managed service immediately after acknowledgement")
	if err := managed.Start(ctx); err != nil {
		return fmt.Errorf("restart service: %w", err)
	}
	r.event("recovery", "service restarted and became ready")
	if responseRule != nil {
		for i, o := range observations {
			value, err := observe(ctx, o)
			if err != nil {
				return fmt.Errorf("after-restart observation %q: %w", r.Checks[i].Name, err)
			}
			check := r.Checks[i]
			check.Observed = &value
			r.AfterRestartChecks = append(r.AfterRestartChecks, check)
			r.event("after restart", fmt.Sprintf("%s: expected %d, observed %d before retry", check.Name, check.Expected, value))
		}
		start = time.Now()
		retryStatus, retryBody, retryErr := sendWithBody(ctx, request, request.URL, captureResponse)
		r.Attempts = append(r.Attempts, newAttempt("retry", retryStatus, retryErr, start))
		if retryErr != nil {
			return fmt.Errorf("retry after restart: %w", retryErr)
		}
		r.event("retry", fmt.Sprintf("resent the same operation after restart; HTTP %d", retryStatus))
		check, err := checkRetryResponse(responseRule, status, originalBody, retryStatus, retryBody)
		if err != nil {
			return fmt.Errorf("compare post-restart retry response: %w", err)
		}
		r.RetryResponse = &check
		if check.Passed {
			r.event("retry check", "post-restart reply met the declared checks")
		} else {
			r.event("retry check", strings.Join(check.Issues, "; "))
		}
	}
	return nil
}

// wanted names the acceptable values of a check: "1", or "0 or 1".
func wanted(c StateCheck) string {
	values := []int64{c.Expected}
	for _, v := range c.Allowed {
		if !slices.Contains(values, v) {
			values = append(values, v)
		}
	}
	slices.Sort(values)
	parts := make([]string, len(values))
	for i, v := range values {
		parts[i] = strconv.FormatInt(v, 10)
	}
	return strings.Join(parts, " or ")
}

func capturesReply(rule *config.RetryResponse) bool {
	return rule != nil && (len(rule.SameJSONPointers) > 0 || rule.CompareIDs)
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func send(ctx context.Context, req config.Request, target string) (int, error) {
	status, _, err := sendWithBody(ctx, req, target, false)
	return status, err
}

func sendWithBody(ctx context.Context, req config.Request, target string, capture bool) (int, []byte, error) {
	status, body, _, err := sendTimed(ctx, req, target, capture)
	return status, body, err
}

// sendTimed also reports how long the service took to start answering,
// measured from the moment the request had been written.
func sendTimed(ctx context.Context, req config.Request, target string, capture bool) (status int, data []byte, wait time.Duration, err error) {
	var body io.Reader
	if len(req.Body) > 0 {
		body = bytes.NewReader(req.Body)
	}
	var wrote, answered atomic.Int64
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		WroteRequest:         func(httptrace.WroteRequestInfo) { wrote.CompareAndSwap(0, time.Now().UnixNano()) },
		GotFirstResponseByte: func() { answered.CompareAndSwap(0, time.Now().UnixNano()) },
	})
	defer func() {
		if w, a := wrote.Load(), answered.Load(); w > 0 && a > w {
			wait = time.Duration(a - w)
		}
	}()
	httpReq, err := http.NewRequestWithContext(ctx, strings.ToUpper(req.Method), target, body)
	if err != nil {
		return 0, nil, 0, err
	}
	setHeaders(httpReq, req.Headers)
	if body != nil && httpReq.Header.Get("Content-Type") == "" {
		// Bodies are always JSON. Most frameworks ignore one that arrives
		// without a content type, which would look like a failed write.
		httpReq.Header.Set("Content-Type", "application/json")
	}
	client := localClient()
	resp, err := client.Do(httpReq)
	if err != nil {
		return 0, nil, 0, err
	}
	defer resp.Body.Close()
	if capture {
		data, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
		if err != nil {
			return resp.StatusCode, nil, 0, fmt.Errorf("read response body: %w", err)
		}
		if len(data) > 1<<20 {
			return resp.StatusCode, nil, 0, errors.New("response body exceeds 1 MiB")
		}
		return resp.StatusCode, data, 0, nil
	}
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		return resp.StatusCode, nil, 0, fmt.Errorf("read response body: %w", err)
	}
	return resp.StatusCode, nil, 0, nil
}

// setHeaders applies scenario headers. Go sends the Host header from the
// request's Host field and ignores one set in the header map, so a scenario
// that routes by virtual host needs it copied across.
func setHeaders(req *http.Request, headers map[string]string) {
	for k, v := range headers {
		if strings.EqualFold(k, "Host") {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
}

// Each operation gets a direct, fresh connection. This keeps proxy settings
// and automatic retries on pooled connections out of the experiment.
func localClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DisableKeepAlives = true
	return &http.Client{
		Transport:     transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func observe(ctx context.Context, o config.Observation) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.URL, nil)
	if err != nil {
		return 0, err
	}
	setHeaders(req, o.Headers)
	client := localClient()
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil {
		return 0, fmt.Errorf("read observation: %w", err)
	}
	if len(data) > 1<<20 {
		return 0, errors.New("observation exceeds 1 MiB")
	}
	if err := strictjson.RejectDuplicateKeys(data); err != nil {
		return 0, fmt.Errorf("invalid observation JSON: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var root any
	if err := dec.Decode(&root); err != nil {
		return 0, err
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		if err == nil {
			return 0, errors.New("observation contains multiple JSON values")
		}
		return 0, fmt.Errorf("trailing observation JSON: %w", err)
	}
	value, err := atPointer(root, o.Pointer)
	if err != nil {
		return 0, err
	}
	if o.Counts() == config.MeasureLength {
		if value == nil {
			return 0, nil // Go and some ORMs encode an empty list as null.
		}
		items, ok := value.([]any)
		if !ok {
			return 0, fmt.Errorf("value at %q is not a JSON array", o.Pointer)
		}
		return int64(len(items)), nil
	}
	num, ok := value.(json.Number)
	if !ok {
		return 0, fmt.Errorf("value at %q is not a JSON integer", o.Pointer)
	}
	n, err := num.Int64()
	if err != nil {
		return 0, fmt.Errorf("value at %q is not an int64: %w", o.Pointer, err)
	}
	return n, nil
}

func atPointer(root any, pointer string) (any, error) {
	if !config.ValidJSONPointer(pointer) {
		return nil, errors.New("invalid RFC 6901 JSON pointer")
	}
	if pointer == "" {
		return root, nil
	}
	current := root
	for _, part := range strings.Split(pointer[1:], "/") {
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		switch node := current.(type) {
		case map[string]any:
			value, ok := node[part]
			if !ok {
				return nil, fmt.Errorf("JSON pointer %q: key %q not found", pointer, part)
			}
			current = value
		case []any:
			if part == "" || (len(part) > 1 && part[0] == '0') {
				return nil, fmt.Errorf("JSON pointer %q: invalid array index %q", pointer, part)
			}
			for _, digit := range part {
				if digit < '0' || digit > '9' {
					return nil, fmt.Errorf("JSON pointer %q: invalid array index %q", pointer, part)
				}
			}
			index, err := strconv.Atoi(part)
			if err != nil || index < 0 || index >= len(node) {
				return nil, fmt.Errorf("JSON pointer %q: invalid array index %q", pointer, part)
			}
			current = node[index]
		default:
			return nil, fmt.Errorf("JSON pointer %q traverses a scalar", pointer)
		}
	}
	return current, nil
}
