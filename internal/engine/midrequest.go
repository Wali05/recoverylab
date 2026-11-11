package engine

import (
	"context"
	"fmt"
	"net/http/httptrace"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/Wali05/recoverylab/internal/config"
)

// runCrashMidRequest kills the service while it is still handling the write,
// restarts it, and checks two things. First, the crash must leave the write
// either done or not done: every effect, or none. Second, retrying must
// finish the job exactly once.
//
// The kill is timed from the moment the request has been written. If the
// service manages to answer first, the write was acknowledged and has to
// have survived, exactly as in crash-after-ack.
func runCrashMidRequest(ctx context.Context, request config.Request, responseRule *config.RetryResponse, managed *process, observations []config.Observation, delay time.Duration, r *Report) error {
	type reply struct {
		status int
		body   []byte
		err    error
	}
	sent := make(chan struct{})
	done := make(chan reply, 1)
	var once sync.Once
	trace := &httptrace.ClientTrace{WroteRequest: func(httptrace.WroteRequestInfo) { once.Do(func() { close(sent) }) }}
	capture := capturesReply(responseRule)
	start := time.Now()
	go func() {
		status, body, err := sendWithBody(httptrace.WithClientTrace(ctx, trace), request, request.URL, capture)
		done <- reply{status, body, err}
	}()

	// Wait for the request to leave, then for the crash point. A reply that
	// arrives in the meantime ends the wait early.
	var got *reply
	wroteAt := start
	select {
	case <-sent:
		wroteAt = time.Now()
		// Timers are only good to about a millisecond, and many handlers
		// finish in less. Sleep for most of a long delay, then spin for the
		// rest so the kill lands when it was asked to.
		deadline := wroteAt.Add(delay)
		if coarse := delay - 2*time.Millisecond; coarse > 0 {
			timer := time.NewTimer(coarse)
			select {
			case <-timer.C:
			case rep := <-done:
				timer.Stop()
				got = &rep
			case <-ctx.Done():
				timer.Stop()
				return fmt.Errorf("wait for the crash point: %w", ctx.Err())
			}
		}
		for got == nil && time.Now().Before(deadline) {
			select {
			case rep := <-done:
				got = &rep
			case <-ctx.Done():
				return fmt.Errorf("wait for the crash point: %w", ctx.Err())
			default:
				runtime.Gosched()
			}
		}
	case rep := <-done:
		got = &rep
	case <-ctx.Done():
		return fmt.Errorf("send original request: %w", ctx.Err())
	}
	if got != nil && got.err != nil {
		r.Attempts = append(r.Attempts, newAttempt("original", got.status, got.err, start))
		return fmt.Errorf("original request failed before the crash point: %w", got.err)
	}

	killedAfter := time.Since(wroteAt)
	crashErr := managed.Crash()
	if got == nil {
		// The kill and the reply can cross. A complete reply still counts.
		select {
		case rep := <-done:
			got = &rep
		case <-ctx.Done():
			return fmt.Errorf("wait for the interrupted request: %w", ctx.Err())
		}
	}
	r.Attempts = append(r.Attempts, newAttempt("original", got.status, got.err, start))
	acknowledged := got.err == nil
	if acknowledged && (got.status < 200 || got.status >= 300) {
		return fmt.Errorf("service did not acknowledge operation: HTTP %d", got.status)
	}
	if crashErr != nil {
		return fmt.Errorf("crash service: %w", crashErr)
	}
	r.FaultInjected = true
	point := CrashPointCheck{DelayMS: millis(killedAfter), ReplyReceived: acknowledged}
	if acknowledged {
		r.event("fault", fmt.Sprintf("killed the managed service %s after the request was sent; it had already answered HTTP %d", brief(killedAfter), got.status))
	} else {
		r.event("fault", fmt.Sprintf("killed the managed service %s after the request was sent, before any reply", brief(killedAfter)))
	}

	if err := managed.Start(ctx); err != nil {
		r.CrashPoint = &point
		return fmt.Errorf("restart service: %w", err)
	}
	r.event("recovery", "service restarted and became ready")

	// Look before retrying: a retry that finishes a half-done write would
	// otherwise hide what the crash left behind.
	var applied, missing []string
	strange := false
	for i, o := range observations {
		value, err := observe(ctx, o)
		if err != nil {
			r.CrashPoint = &point
			return fmt.Errorf("after-restart observation %q: %w", r.Checks[i].Name, err)
		}
		check := r.Checks[i]
		check.Observed = &value
		if !acknowledged {
			check.Allowed = []int64{check.Baseline, check.Expected}
		}
		r.AfterRestartChecks = append(r.AfterRestartChecks, check)
		r.event("after restart", fmt.Sprintf("%s: observed %d before retry (acceptable: %s)", check.Name, value, wanted(check)))
		switch {
		case check.Baseline == check.Expected:
			// This effect isn't supposed to move, so it says nothing either
			// way about how far the write got.
			strange = strange || value != check.Expected
		case value == check.Expected:
			applied = append(applied, check.Name)
		case value == check.Baseline:
			missing = append(missing, check.Name)
		default:
			strange = true
		}
	}
	switch {
	case strange:
		// The after-restart comparison reports the exact values.
		point.Outcome = "unexpected"
	case len(applied) > 0 && len(missing) > 0:
		point.Outcome = "partial"
		point.Issues = append(point.Issues, fmt.Sprintf("the crash left a half-finished write: %s changed but %s did not", strings.Join(applied, ", "), strings.Join(missing, ", ")))
	case len(missing) > 0:
		point.Outcome = "not-applied"
	default:
		point.Outcome = "applied"
	}
	// Losing a write is only acceptable if the service never said it worked.
	point.Passed = point.Outcome == "applied" || (point.Outcome == "not-applied" && !acknowledged)
	r.CrashPoint = &point
	r.event("crash point", "after the crash the write was "+map[string]string{
		"applied": "fully applied", "not-applied": "not applied at all", "partial": "half applied", "unexpected": "in a state that is neither before nor after",
	}[point.Outcome])

	retryStart := time.Now()
	retryStatus, retryBody, retryErr := sendWithBody(ctx, request, request.URL, capture)
	r.Attempts = append(r.Attempts, newAttempt("retry", retryStatus, retryErr, retryStart))
	if retryErr != nil {
		return fmt.Errorf("retry after restart: %w", retryErr)
	}
	r.event("retry", fmt.Sprintf("resent the same operation after restart; HTTP %d", retryStatus))
	rule := responseRule
	if rule != nil && !acknowledged {
		// With no first reply there is nothing to compare the retry's IDs to.
		statusOnly := config.RetryResponse{AllowedStatuses: rule.AllowedStatuses}
		rule = &statusOnly
	}
	check, err := checkRetryResponse(rule, got.status, got.body, retryStatus, retryBody)
	if err != nil {
		return fmt.Errorf("compare post-restart retry response: %w", err)
	}
	r.RetryResponse = &check
	if check.Passed {
		r.event("retry check", "post-restart reply met the declared checks")
	} else {
		r.event("retry check", strings.Join(check.Issues, "; "))
	}
	return nil
}

// brief formats a delay for a sentence: "0.4 ms", "12 ms", "1.5 s".
func brief(d time.Duration) string {
	switch {
	case d < 10*time.Millisecond:
		return fmt.Sprintf("%.1f ms", float64(d.Microseconds())/1000)
	case d < time.Second:
		return fmt.Sprintf("%d ms", d.Milliseconds())
	default:
		return fmt.Sprintf("%.1f s", d.Seconds())
	}
}
