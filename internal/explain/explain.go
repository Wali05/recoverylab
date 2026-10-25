// Package explain turns a report into plain language: what went wrong, the
// most likely reason, and what usually fixes it. It only reads the report, so
// every sentence has to be backed by something RecoveryLab actually observed.
package explain

import (
	"fmt"
	"strings"

	"github.com/Wali05/recoverylab/internal/engine"
)

type Diagnosis struct {
	Summary string   `json:"summary"`
	Cause   string   `json:"cause,omitempty"`
	Fixes   []string `json:"fixes,omitempty"`
}

func Report(r engine.Report) Diagnosis {
	switch r.Status {
	case "PASS":
		return pass(r)
	case "VIOLATION":
		switch r.Scenario {
		case "lost-response":
			return lostResponse(r)
		case "concurrent-duplicates":
			return concurrent(r)
		case "crash-after-ack":
			return crash(r)
		case "key-reuse":
			return keyReuse(r)
		}
		return Diagnosis{Summary: "A declared check failed."}
	default:
		return inconclusive(r)
	}
}

func pass(r engine.Report) Diagnosis {
	switch r.Scenario {
	case "lost-response":
		return Diagnosis{Summary: "The client lost the reply and retried; the write still happened exactly once."}
	case "concurrent-duplicates":
		return Diagnosis{Summary: fmt.Sprintf("%d copies arrived together and the write still happened exactly once.", len(r.Attempts))}
	case "crash-after-ack":
		if r.RetryResponse != nil {
			return Diagnosis{Summary: "The acknowledged write survived a crash, and the retry after restart did not repeat it."}
		}
		return Diagnosis{Summary: "The acknowledged write survived a process crash and restart."}
	case "key-reuse":
		return Diagnosis{Summary: "Reusing the key for a different request was rejected and nothing extra was written."}
	}
	return Diagnosis{Summary: "Every declared check held."}
}

// missed returns the first check that didn't hold. Checks keep scenario order,
// so this is the effect the user listed first.
func missed(checks []engine.StateCheck) (engine.StateCheck, bool) {
	for _, c := range checks {
		if c.Observed != nil && *c.Observed != c.Expected {
			return c, true
		}
	}
	return engine.StateCheck{}, false
}

func effects(c engine.StateCheck) string {
	got := *c.Observed - c.Baseline
	want := c.Expected - c.Baseline
	return fmt.Sprintf("%s changed by %d; it should have changed by %d", c.Name, got, want)
}

func lostResponse(r engine.Report) Diagnosis {
	if c, ok := missed(r.Checks); ok {
		if *c.Observed > c.Expected {
			return Diagnosis{
				Summary: "The retry ran the write a second time (" + effects(c) + ").",
				Cause:   "The service doesn't recognize the retry as the same operation. Either it doesn't store idempotency keys, or the key isn't part of the request it checks.",
				Fixes: []string{
					"Store the idempotency key alongside the effect, in the same transaction, with a unique constraint on the key.",
					"When a key you've already completed comes back, return the stored response instead of running the handler again.",
				},
			}
		}
		return Diagnosis{
			Summary: "Fewer effects than expected after the retry (" + effects(c) + ").",
			Cause:   "Something undid or skipped the original write, or expected_delta doesn't match what one request should do.",
			Fixes:   []string{"Check expected_delta first. If it's right, look for a retry path that deletes or replaces the original record."},
		}
	}
	return retryReply(r, "lost-response")
}

func retryReply(r engine.Report, scenario string) Diagnosis {
	rr := r.RetryResponse
	if rr == nil || rr.Passed {
		return Diagnosis{Summary: "A declared check failed."}
	}
	when := "the retry"
	if scenario == "crash-after-ack" {
		when = "the retry after restart"
	}
	if !statusAllowed(rr) {
		d := Diagnosis{
			Summary: fmt.Sprintf("The write happened once, but %s got HTTP %d.", when, rr.RetryHTTPStatus),
			Cause:   "A client that sees this will think its request failed, even though it worked. It may give up, show an error, or try something worse.",
			Fixes: []string{
				"For a completed key, return the original status and body again.",
				"If your API contract really does answer duplicates with this code, declare it in retry_response.allowed_statuses.",
			},
		}
		if rr.RetryHTTPStatus >= 500 {
			d.Cause = "The service probably hit its own uniqueness check (a duplicate-key error, for example) and let it escape as a server error. " + d.Cause
		}
		return d
	}
	return Diagnosis{
		Summary: fmt.Sprintf("The write happened once, but %s returned a different %s.", when, strings.Join(rr.SameJSONPointers, ", ")),
		Cause:   "The service handled the duplicate but built a fresh reply. The client may now hold two IDs for one thing.",
		Fixes:   []string{"Save the original response body with the idempotency key and replay it byte for byte."},
	}
}

func statusAllowed(rr *engine.ResponseCheck) bool {
	for _, issue := range rr.Issues {
		if strings.HasPrefix(issue, "retry returned HTTP") {
			return false
		}
	}
	return true
}

func concurrent(r engine.Report) Diagnosis {
	n := len(r.Attempts)
	if c, ok := missed(r.Checks); ok && *c.Observed > c.Expected {
		return Diagnosis{
			Summary: fmt.Sprintf("%d identical requests arrived together and the write happened %d times (%s).", n, *c.Observed-c.Baseline, c.Name),
			Cause:   "This is a check-then-write race. Several requests asked \"have I seen this key?\" before any of them recorded it, so they all went ahead.",
			Fixes: []string{
				"Make claiming the key atomic: a unique index on the key plus INSERT ... ON CONFLICT DO NOTHING, or a row lock, before doing the work.",
				"Requests that lose the race should wait for the winner and replay its result, or return 409 while it is still running.",
			},
		}
	} else if ok {
		return Diagnosis{
			Summary: fmt.Sprintf("After %d overlapping requests, %s.", n, effects(c)),
			Cause:   "The overlapping requests interfered with each other, or expected_delta doesn't match one operation.",
			Fixes:   []string{"Check expected_delta, then look for a lock or transaction that rolls back the winner when it collides with a loser."},
		}
	}
	if cr := r.ConcurrentResponse; cr != nil && !cr.Passed {
		return Diagnosis{
			Summary: "The write happened once, but some copies got a status your API doesn't promise: " + strings.Join(cr.Issues, "; ") + ".",
			Cause:   "Losing requests usually hit the uniqueness check while the winner was still running and the error escaped unhandled.",
			Fixes: []string{
				"Catch the duplicate-key error and answer with 409 (in progress) or the winner's stored reply.",
				"If these codes are part of your contract, add them to concurrent_response.allowed_statuses.",
			},
		}
	}
	return Diagnosis{Summary: "A declared check failed."}
}

func crash(r engine.Report) Diagnosis {
	if c, ok := missed(r.AfterRestartChecks); ok && *c.Observed < c.Expected {
		return lostAck(c, r.RetryResponse != nil)
	}
	if c, ok := missed(r.Checks); ok {
		if *c.Observed < c.Expected && len(r.AfterRestartChecks) == 0 {
			return lostAck(c, false)
		}
		if *c.Observed > c.Expected {
			return Diagnosis{
				Summary: "The write survived the crash, but retrying after restart ran it again (" + effects(c) + ").",
				Cause:   "The service forgot which keys it had already handled when it restarted. Keys kept in memory or a local cache disappear with the process.",
				Fixes:   []string{"Keep idempotency keys in the same durable store as the effect and commit them together."},
			}
		}
		return Diagnosis{Summary: "After the crash, " + effects(c) + "."}
	}
	return retryReply(r, "crash-after-ack")
}

func lostAck(c engine.StateCheck, retried bool) Diagnosis {
	d := Diagnosis{
		Summary: "The service said 2xx, then lost the write when it crashed (" + effects(c) + ").",
		Cause:   "It replied before the write was durable: the work was still in memory, in a background job, or in an uncommitted transaction.",
		Fixes: []string{
			"Commit the write (and flush it, if you manage files yourself) before sending the success response.",
			"If the work really has to happen later, answer 202 Accepted and give the client a way to check on it.",
		},
	}
	if retried {
		d.Cause += " RecoveryLab checked before retrying, so a retry that recreates the write can't hide this."
	}
	return d
}

func keyReuse(r engine.Report) Diagnosis {
	if c, ok := missed(r.Checks); ok && *c.Observed > c.Expected {
		return Diagnosis{
			Summary: "A different request with an already-used key created another effect (" + effects(c) + ").",
			Cause:   "The service doesn't remember what the key was first used for, so it can't tell a retry from a new request that reused the key by mistake.",
			Fixes:   []string{"Store a fingerprint of the request (method, path, and body) with the key. If a key comes back with a different fingerprint, reject it with 422 and write nothing."},
		}
	}
	kr := r.KeyReuseResponse
	if kr == nil || kr.Passed {
		if c, ok := missed(r.Checks); ok {
			return Diagnosis{Summary: "After the changed request, " + effects(c) + "."}
		}
		return Diagnosis{Summary: "A declared check failed."}
	}
	if kr.ActualHTTPStatus >= 200 && kr.ActualHTTPStatus < 300 {
		return Diagnosis{
			Summary: fmt.Sprintf("The changed request got HTTP %d, but nothing new was written.", kr.ActualHTTPStatus),
			Cause:   "The service replayed the first result for a different request. The client believes its changed request went through when it didn't.",
			Fixes:   []string{fmt.Sprintf("Compare a fingerprint of the request body with the one stored for the key, and answer HTTP %d when they differ.", kr.ExpectedHTTPStatus)},
		}
	}
	return Diagnosis{
		Summary: fmt.Sprintf("The changed request was answered with HTTP %d; the scenario expected HTTP %d.", kr.ActualHTTPStatus, kr.ExpectedHTTPStatus),
		Cause:   "Nothing extra was written, so this may only be a different status code than the scenario assumes.",
		Fixes:   []string{fmt.Sprintf("If HTTP %d is what your API promises, set \"expected_reuse_status\": %d.", kr.ActualHTTPStatus, kr.ActualHTTPStatus)},
	}
}

func inconclusive(r engine.Report) Diagnosis {
	d := Diagnosis{Summary: "RecoveryLab couldn't finish the experiment, so there's no verdict either way."}
	e := strings.ToLower(r.Error)
	switch {
	case strings.Contains(e, "connection refused") || strings.Contains(e, "actively refused"):
		d.Cause = "Nothing is listening at one of the URLs in the scenario."
		d.Fixes = []string{"Start the service first, or check the host and port in request.url and the state URL."}
	case strings.Contains(e, "already responding before the managed service started"):
		d.Cause = "Something is already running on the service's port, so RecoveryLab can't be sure it's testing the process it started."
		d.Fixes = []string{"Stop the other server, or move the managed service to a free port."}
	case strings.Contains(e, "is not a json integer") || strings.Contains(e, "is not an int64"):
		d.Cause = "The state pointer reaches something that isn't a whole number."
		d.Fixes = []string{"If it points at a list, add \"measure\": \"length\" to count its items.", "Run recoverylab init --state URL to see which counters the endpoint exposes."}
	case strings.Contains(e, "is not a json array"):
		d.Cause = "measure is set to length, but the pointer reaches something that isn't a list."
		d.Fixes = []string{"Point at the array itself, such as /data or /items, or remove measure to read a number."}
	case strings.Contains(e, "not found") && strings.Contains(e, "json pointer"):
		d.Cause = "The state response doesn't have the field the pointer names."
		d.Fixes = []string{"Run recoverylab init --state URL to list the counters it can see."}
	case strings.Contains(e, "did not acknowledge"):
		d.Cause = "The very first request didn't succeed, so there was nothing to retry."
		d.Fixes = []string{"Send the same request with curl and fix the body, headers, or auth until it returns 2xx."}
	case strings.Contains(e, "readiness timeout") || strings.Contains(e, "exited before readiness"):
		d.Cause = "The managed service didn't become ready in time."
		d.Fixes = []string{"Check the command and its logs above. Raise service.startup_timeout if it is just slow."}
	case strings.Contains(e, "deadline exceeded"):
		d.Cause = "The experiment ran out of time."
		d.Fixes = []string{"Raise timeout, or check for a request that hangs."}
	case strings.Contains(e, "baseline observation") && strings.Contains(e, "http "):
		d.Cause = "The state endpoint answered with an error."
		d.Fixes = []string{"Check its URL, and add observe.headers if it needs auth."}
	}
	return d
}
