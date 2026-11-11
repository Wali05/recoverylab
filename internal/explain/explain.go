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
	// Learn links to the section of the fixing guide with a worked example.
	Learn string `json:"learn_more,omitempty"`
}

// Guide is the fixing guide's address. It points at main so links in old
// reports keep working after the guide improves.
const Guide = "https://github.com/Wali05/recoverylab/blob/main/docs/fixing.md"

func guide(anchor string) string { return Guide + "#" + anchor }

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
		case "crash-mid-request":
			return midRequest(r)
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
	case "crash-mid-request":
		cp := r.CrashPoint
		switch {
		case cp != nil && cp.ReplyReceived:
			return Diagnosis{Summary: "The service answered just before it was killed. The write survived, and the retry did not repeat it."}
		case cp != nil && cp.Outcome == "not-applied":
			return Diagnosis{Summary: "The service was killed mid-request before anything was written, and the retry then did the write exactly once."}
		}
		return Diagnosis{Summary: "The service was killed mid-request after the write had committed, and the retry did not repeat it."}
	case "key-reuse":
		return Diagnosis{Summary: "Reusing the key for a different request was rejected and nothing extra was written."}
	}
	return Diagnosis{Summary: "Every declared check held."}
}

// CrashSweep sums up several crash-mid-request runs that all passed. It says
// how many kills really landed mid-request, because a pass where every kill
// came after the reply only repeats what crash-after-ack already showed.
func CrashSweep(reports []engine.Report) Diagnosis {
	kills, mid := 0, 0
	for _, r := range reports {
		if r.CrashPoint != nil {
			kills++
			if !r.CrashPoint.ReplyReceived {
				mid++
			}
		}
	}
	switch {
	case kills <= 1:
		return pass(reports[0])
	case mid == 0:
		return Diagnosis{Summary: fmt.Sprintf("The service was killed %d times, but it answered before every kill, so none landed mid-request. Each acknowledged write survived and no retry repeated it.", kills)}
	}
	return Diagnosis{Summary: fmt.Sprintf("The service was killed at %d different moments, %d of them mid-request. The write was never left half-done, and every retry finished it exactly once.", kills, mid)}
}

// missed returns the first check that didn't hold. Checks keep scenario order,
// so this is the effect the user listed first.
func missed(checks []engine.StateCheck) (engine.StateCheck, bool) {
	for _, c := range checks {
		if !c.Held() {
			return c, true
		}
	}
	return engine.StateCheck{}, false
}

func effects(c engine.StateCheck) string {
	return fmt.Sprintf("%s changed by %d instead of %d", label(c), *c.Observed-c.Baseline, c.Expected-c.Baseline)
}

// label names a check in a sentence. Single-observation scenarios call
// their check "state", which reads better as "the count".
func label(c engine.StateCheck) string {
	if c.Name == "state" {
		return "the count"
	}
	return c.Name
}

// unchanged spots a 2xx with no visible effect at all. That points at the
// measurement or an asynchronous write rather than at retry handling.
func unchanged(checks []engine.StateCheck) (Diagnosis, bool) {
	c, ok := missed(checks)
	if !ok || *c.Observed != c.Baseline {
		return Diagnosis{}, false
	}
	d := Diagnosis{
		Summary: fmt.Sprintf("The service answered 2xx, but %s didn't change at all.", label(c)),
		Learn:   guide("reply-after-the-write-commits"),
		Cause:   "Either the write happens later (a queue or background job), or the state endpoint isn't measuring what this request writes. RecoveryLab reads state as soon as the requests finish.",
		Fixes: []string{
			"Check that the state URL and pointer reflect this request: send it once with curl and watch the value.",
			"If the write is asynchronous, reply only after it commits, or answer 202 and expose a count that includes pending work.",
		},
	}
	if c.Measure == "length" && fullPage(c.Baseline) {
		// A full page stays the same length however many rows exist.
		d.Cause = fmt.Sprintf("The list already had %d items, which looks like one full page. A page that is full stays the same length when something is added. Otherwise: ", c.Baseline) +
			strings.ToLower(d.Cause[:1]) + d.Cause[1:]
		d.Fixes = append([]string{"Count a total instead of a page: point at a field such as /total, or ask for a page larger than the test data (for example ?limit=1000)."}, d.Fixes...)
	}
	return d, true
}

// fullPage reports whether n is a common default page size.
func fullPage(n int64) bool {
	switch n {
	case 10, 15, 20, 25, 30, 50, 100, 200, 250, 500, 1000:
		return true
	}
	return false
}

func lostResponse(r engine.Report) Diagnosis {
	if d, ok := unchanged(r.Checks); ok {
		return d
	}
	if c, ok := missed(r.Checks); ok {
		if *c.Observed > c.Expected {
			return Diagnosis{
				Summary: "The retry ran the write a second time (" + effects(c) + ").",
				Learn:   guide("store-the-key-with-the-effect"),
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
			Learn:   guide("replay-the-first-response"),
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
		Learn:   guide("replay-the-first-response"),
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
	if d, ok := unchanged(r.Checks); ok {
		return d
	}
	n := len(r.Attempts)
	if c, ok := missed(r.Checks); ok && *c.Observed > c.Expected {
		return Diagnosis{
			Summary: fmt.Sprintf("%d identical requests arrived together and %s went up by %d instead of %d.", n, label(c), *c.Observed-c.Baseline, c.Expected-c.Baseline),
			Learn:   guide("claim-the-key-atomically"),
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
	if cr := r.ConcurrentResponse; cr != nil && !cr.Passed && cr.NoServerErrors && len(cr.AllowedStatuses) == 0 {
		return Diagnosis{
			Summary: "The write happened once, but some copies got a server error: " + strings.Join(cr.Issues, "; ") + ".",
			Learn:   guide("claim-the-key-atomically"),
			Cause:   "The losing requests most likely hit a unique-key error while the first one was still running, and it escaped as a 500. Clients usually retry a 500, which piles more load on the same race.",
			Fixes: []string{
				"Catch the duplicate-key error and answer 409 Conflict, which tells the client the request is still in progress and can be retried shortly.",
				"Once the first request finishes, replay its stored result for the same key.",
			},
		}
	}
	if cr := r.ConcurrentResponse; cr != nil && !cr.Passed {
		return Diagnosis{
			Summary: "The write happened once, but some copies got a status your API doesn't promise: " + strings.Join(cr.Issues, "; ") + ".",
			Learn:   guide("claim-the-key-atomically"),
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
				Learn:   guide("keep-keys-across-restarts"),
				Cause:   "The service forgot which keys it had already handled when it restarted. Keys kept in memory or a local cache disappear with the process.",
				Fixes:   []string{"Keep idempotency keys in the same durable store as the effect and commit them together."},
			}
		}
		return Diagnosis{Summary: "After the crash, " + effects(c) + "."}
	}
	return retryReply(r, "crash-after-ack")
}

// midRequest explains a crash while the request was still being handled. It
// reads the outcome in order: what the crash left behind, then what the
// retry did about it.
func midRequest(r engine.Report) Diagnosis {
	cp := r.CrashPoint
	if cp == nil {
		return Diagnosis{Summary: "A declared check failed."}
	}
	if cp.Outcome == "partial" {
		var done, undone []string
		for _, c := range r.AfterRestartChecks {
			if c.Observed == nil || c.Baseline == c.Expected {
				continue
			}
			if *c.Observed == c.Expected {
				done = append(done, label(c))
			} else {
				undone = append(undone, label(c))
			}
		}
		d := Diagnosis{
			Summary: fmt.Sprintf("The crash left a half-finished write: %s changed but %s did not.", strings.Join(done, ", "), strings.Join(undone, ", ")),
			Learn:   guide("commit-every-effect-together"),
			Cause:   "These effects aren't committed together. The handler finished one write and the process died before the next, and nothing rolls the first one back.",
			Fixes: []string{
				"Put every write for one request in a single database transaction, so a crash leaves all of them or none.",
				"If one effect lives in another system (a payment provider, a queue), record the intent in that same transaction and finish it on recovery.",
			},
		}
		if _, still := missed(r.Checks); still {
			d.Cause += " The retry didn't repair it either: the key was already recorded, so the service treated the request as done."
		}
		return d
	}
	if c, ok := missed(r.AfterRestartChecks); ok {
		if cp.ReplyReceived && *c.Observed < c.Expected {
			return lostAck(c, true)
		}
		return Diagnosis{
			Summary: fmt.Sprintf("After the crash, %s was %d, which is neither the value before the request (%d) nor after it (%d).", label(c), *c.Observed, c.Baseline, c.Expected),
			Cause:   "Recovery left the data in a state no request could have produced, or expected_delta doesn't match what one request does.",
			Fixes:   []string{"Check expected_delta first. If it's right, look at what the service does on startup with work it finds unfinished."},
		}
	}
	// From here on the crash itself was survived cleanly; the retry went wrong.
	// A refused retry comes first: it also leaves the write undone, and the
	// refusal is the better clue to why.
	if rr := r.RetryResponse; rr != nil && !rr.Passed && !statusAllowed(rr) && cp.Outcome == "not-applied" {
		return Diagnosis{
			Summary: fmt.Sprintf("The crash stopped the write before it happened, and the retry was refused with HTTP %d.", rr.RetryHTTPStatus),
			Learn:   guide("dont-leave-a-key-claimed-by-a-dead-request"),
			Cause:   "The request that died left its key marked as in progress, and nothing clears it. Every retry now collides with a request that no longer exists.",
			Fixes: []string{
				"Claim the key in the same transaction as the work, so a crash rolls both back.",
				"Or give an in-progress claim a timeout, and let a retry take it over once the request that made it is gone.",
			},
		}
	}
	if c, ok := missed(r.Checks); ok {
		switch {
		case *c.Observed > c.Expected:
			return Diagnosis{
				Summary: "The write committed before the crash, and the retry ran it again (" + effects(c) + ").",
				Learn:   guide("store-the-key-with-the-effect"),
				Cause:   "The work was saved but its idempotency key wasn't, so after the restart the retry looked like a new request. Either the two are written separately, or keys live in memory.",
				Fixes:   []string{"Write the key and the effect in the same transaction, in the same durable store."},
			}
		case cp.Outcome == "not-applied":
			d := Diagnosis{
				Summary: "The crash stopped the write before it happened, and the retry was told it was done (" + effects(c) + ").",
				Learn:   guide("dont-leave-a-key-claimed-by-a-dead-request"),
				Cause:   "The key was saved before the work it stands for. After the restart the service found the key and assumed the work existed.",
				Fixes: []string{
					"Save the key and the effect in one transaction. A key must never be visible without its effect.",
					"If the key has to be claimed first, mark it in progress and let a retry redo the work when the request that claimed it is gone.",
				},
			}
			if rr := r.RetryResponse; rr != nil {
				d.Summary = fmt.Sprintf("The crash stopped the write before it happened, and the retry got HTTP %d without doing it (%s).", rr.RetryHTTPStatus, effects(c))
			}
			return d
		}
		return Diagnosis{Summary: "After the crash and the retry, " + effects(c) + "."}
	}
	return retryReply(r, "crash-after-ack")
}

func lostAck(c engine.StateCheck, retried bool) Diagnosis {
	d := Diagnosis{
		Summary: "The service said 2xx, then lost the write when it crashed (" + effects(c) + ").",
		Learn:   guide("reply-after-the-write-commits"),
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
	if d, ok := unchanged(r.Checks); ok {
		return d
	}
	if c, ok := missed(r.Checks); ok && *c.Observed > c.Expected {
		return Diagnosis{
			Summary: "A different request with an already-used key created another effect (" + effects(c) + ").",
			Learn:   guide("bind-the-key-to-the-request"),
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
			Learn:   guide("bind-the-key-to-the-request"),
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
