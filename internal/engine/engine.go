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
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"recoverylab/internal/config"
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
}

type StateCheck struct {
	Name     string `json:"name"`
	Baseline int64  `json:"baseline"`
	Expected int64  `json:"expected"`
	Observed *int64 `json:"observed,omitempty"`
}

type Report struct {
	Name          string       `json:"name"`
	Scenario      string       `json:"scenario"`
	RunID         string       `json:"run_id"`
	Status        string       `json:"status"`
	StartedAt     string       `json:"started_at"`
	DurationMS    int64        `json:"duration_ms"`
	Baseline      *int64       `json:"baseline,omitempty"`
	Expected      *int64       `json:"expected,omitempty"`
	Observed      *int64       `json:"observed,omitempty"`
	Checks        []StateCheck `json:"checks,omitempty"`
	FaultInjected bool         `json:"fault_injected"`
	Attempts      []Attempt    `json:"attempts"`
	Events        []Event      `json:"events"`
	Error         string       `json:"error,omitempty"`
}

func (r *Report) event(step, detail string) {
	r.Events = append(r.Events, Event{At: time.Now().UTC().Format(time.RFC3339Nano), Step: step, Detail: detail})
}

// Run performs one experiment. ERROR means the experiment was inconclusive;
// VIOLATION means it ran and observed a state different from the declared invariant.
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
	c.Resolve(id)
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
		r.Checks = append(r.Checks, StateCheck{Name: label, Baseline: baseline, Expected: expected})
		if len(observations) == 1 {
			r.Baseline = &baseline
			r.Expected = &expected
		}
		r.event("baseline", fmt.Sprintf("%s: observed %d; expected final value %d", label, baseline, expected))
	}

	switch c.Scenario {
	case "lost-response":
		err = runLostResponse(ctx, c.Request, &r)
	case "concurrent-duplicates":
		err = runConcurrent(ctx, c.Request, c.Workers(), &r)
	case "crash-after-ack":
		err = runCrashAfterAck(ctx, c.Request, managed, &r)
	}
	if err != nil {
		r.Error = err.Error()
		return r
	}

	var mismatches []string
	for i, o := range observations {
		final, err := observe(ctx, o)
		if err != nil {
			r.Error = fmt.Sprintf("final observation %q: %v", r.Checks[i].Name, err)
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
		r.Error = "state invariant failed: " + strings.Join(mismatches, "; ")
	} else {
		r.Status = "PASS"
	}
	return r
}

func runConcurrent(ctx context.Context, request config.Request, workers int, r *Report) error {
	r.event("fault", fmt.Sprintf("releasing %d copies of the same operation at once", workers))
	start := make(chan struct{})
	results := make([]Attempt, workers)
	var group sync.WaitGroup
	for i := 0; i < workers; i++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			<-start
			status, err := send(ctx, request, request.URL)
			results[index] = Attempt{Kind: fmt.Sprintf("copy-%02d", index+1), HTTPStatus: status, ClientError: errorString(err)}
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
	return nil
}

func runLostResponse(ctx context.Context, request config.Request, r *Report) error {
	proxy, err := newDropProxy(ctx, request)
	if err != nil {
		return fmt.Errorf("create fault proxy: %w", err)
	}
	defer proxy.Close()
	r.event("fault", "proxy will forward the operation, read the upstream response, then close the client connection without delivering it")
	status, clientErr := send(ctx, request, proxy.URL())
	r.Attempts = append(r.Attempts, Attempt{Kind: "original", HTTPStatus: status, ClientError: errorString(clientErr)})
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
	case <-ctx.Done():
		return fmt.Errorf("wait for fault proxy: %w", ctx.Err())
	}
	retryStatus, retryErr := send(ctx, request, request.URL)
	r.Attempts = append(r.Attempts, Attempt{Kind: "retry", HTTPStatus: retryStatus, ClientError: errorString(retryErr)})
	if retryErr != nil {
		return fmt.Errorf("retry request: %w", retryErr)
	}
	r.event("retry", fmt.Sprintf("resent the same method, body, and idempotency key; HTTP %d", retryStatus))
	return nil
}

func runCrashAfterAck(ctx context.Context, request config.Request, managed *process, r *Report) error {
	status, err := send(ctx, request, request.URL)
	r.Attempts = append(r.Attempts, Attempt{Kind: "original", HTTPStatus: status, ClientError: errorString(err)})
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
	return nil
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func send(ctx context.Context, req config.Request, target string) (int, error) {
	var body io.Reader
	if len(req.Body) > 0 {
		body = bytes.NewReader(req.Body)
	}
	httpReq, err := http.NewRequestWithContext(ctx, strings.ToUpper(req.Method), target, body)
	if err != nil {
		return 0, err
	}
	for k, v := range req.Headers {
		httpReq.Header.Set(k, v)
	}
	client := localClient()
	resp, err := client.Do(httpReq)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		return resp.StatusCode, fmt.Errorf("read response body: %w", err)
	}
	return resp.StatusCode, nil
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
	for k, v := range o.Headers {
		req.Header.Set(k, v)
	}
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
	if err := rejectDuplicateKeys(data); err != nil {
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

func rejectDuplicateKeys(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var scan func() error
	scan = func() error {
		token, err := dec.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for dec.More() {
				keyToken, err := dec.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return errors.New("object member name is not a string")
				}
				if seen[key] {
					return fmt.Errorf("duplicate object member %q", key)
				}
				seen[key] = true
				if err := scan(); err != nil {
					return err
				}
			}
			_, err := dec.Token()
			return err
		case '[':
			for dec.More() {
				if err := scan(); err != nil {
					return err
				}
			}
			_, err := dec.Token()
			return err
		default:
			return errors.New("unexpected closing delimiter")
		}
	}
	return scan()
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
