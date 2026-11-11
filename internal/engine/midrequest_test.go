package engine

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Wali05/recoverylab/internal/config"
)

// helperService starts the fixture in the given mode as a child process the
// engine can kill, and returns its base URL and service block.
func helperService(t *testing.T, mode string) (string, *config.Service) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()
	t.Setenv("RECOVERYLAB_HELPER", "1")
	t.Setenv("RECOVERYLAB_MODE", mode)
	t.Setenv("RECOVERYLAB_ADDR", addr)
	t.Setenv("RECOVERYLAB_JOURNAL", filepath.Join(t.TempDir(), "journal.jsonl"))
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	base := "http://" + addr
	return base, &config.Service{Command: []string{exe, "-test.run=^TestHelperService$"}, ReadyURL: base + "/health", StartupTimeout: "4s"}
}

// twoEffects watches both things one operation is supposed to change.
func twoEffects(base string, service *config.Service, delay string) config.Config {
	one := int64(1)
	c := scenario(base, "crash-mid-request", service)
	c.CrashDelay = delay
	c.Observe = config.Observation{}
	c.Checks = []config.Observation{
		{Name: "operations", URL: base + "/state", Pointer: "/count", ExpectedDelta: &one},
		{Name: "charges", URL: base + "/state", Pointer: "/charges", ExpectedDelta: &one},
	}
	return c
}

func TestCrashMidRequestFindsAHalfFinishedWrite(t *testing.T) {
	base, service := helperService(t, "torn-write-bug")
	// The fixture writes the operation, waits 150 ms, then writes the charge.
	r := Run(context.Background(), twoEffects(base, service, "60ms"))
	if r.Status != "VIOLATION" || !r.FaultInjected || r.CrashPoint == nil {
		t.Fatalf("unexpected report: %+v", r)
	}
	if r.CrashPoint.ReplyReceived || r.CrashPoint.Outcome != "partial" || r.CrashPoint.Passed {
		t.Fatalf("crash point: %+v", r.CrashPoint)
	}
	if !strings.Contains(r.Error, "half-finished write: operations changed but charges did not") {
		t.Fatalf("error should name both effects: %s", r.Error)
	}
	if len(r.AfterRestartChecks) != 2 || *r.AfterRestartChecks[0].Observed != 1 || *r.AfterRestartChecks[1].Observed != 0 {
		t.Fatalf("after restart: %+v", r.AfterRestartChecks)
	}
	// Each value is acceptable on its own. Only together are they wrong.
	for _, check := range r.AfterRestartChecks {
		if !check.Held() {
			t.Errorf("%s should be individually acceptable: %+v", check.Name, check)
		}
	}
	if len(r.Attempts) != 2 || r.Attempts[0].ClientError == "" || r.Attempts[1].HTTPStatus != 200 {
		t.Fatalf("attempts: %+v", r.Attempts)
	}
}

func TestCrashMidRequestFindsAKeyThatOutlivedItsWork(t *testing.T) {
	base, service := helperService(t, "claim-first-bug")
	r := Run(context.Background(), twoEffects(base, service, "60ms"))
	if r.Status != "VIOLATION" || r.CrashPoint == nil || r.CrashPoint.Outcome != "not-applied" || !r.CrashPoint.Passed {
		t.Fatalf("unexpected report: %+v crash point %+v", r, r.CrashPoint)
	}
	// Nothing was written and the retry was told it had been.
	if r.RetryResponse == nil || r.RetryResponse.RetryHTTPStatus != 200 || *r.Checks[0].Observed != r.Checks[0].Baseline {
		t.Fatalf("the retry should have been answered without doing the work: %+v %+v", r.RetryResponse, r.Checks)
	}
}

func TestCrashMidRequestPassesWhenTheWriteIsAtomic(t *testing.T) {
	base, service := helperService(t, "correct")
	for _, delay := range []string{"", "0s", "300us", "2ms", "40ms"} {
		r := Run(context.Background(), twoEffects(base, service, delay))
		if r.Status != "PASS" || !r.FaultInjected || r.CrashPoint == nil || !r.CrashPoint.Passed {
			t.Fatalf("delay %q: %+v crash point %+v", delay, r, r.CrashPoint)
		}
		if out := r.CrashPoint.Outcome; out != "applied" && out != "not-applied" {
			t.Fatalf("delay %q: outcome %q", delay, out)
		}
		if got := *r.Checks[0].Observed - r.Checks[0].Baseline; got != 1 {
			t.Fatalf("delay %q: retry left %d operations, want 1", delay, got)
		}
	}
	// 40 ms is far longer than the fixture needs, so the reply came first and
	// this run was an acknowledged write.
	r := Run(context.Background(), twoEffects(base, service, "40ms"))
	if !r.CrashPoint.ReplyReceived || r.CrashPoint.Outcome != "applied" || len(r.AfterRestartChecks[0].Allowed) != 0 {
		t.Fatalf("a reply before the crash point makes the write acknowledged: %+v", r.CrashPoint)
	}
}

func TestCrashMidRequestTreatsAnEarlyReplyAsAcknowledged(t *testing.T) {
	// early-ack-bug answers 201 at once and writes five seconds later.
	base, service := helperService(t, "early-ack-bug")
	c := scenario(base, "crash-mid-request", service)
	c.CrashDelay = "50ms"
	r := Run(context.Background(), c)
	if r.Status != "VIOLATION" || r.CrashPoint == nil || !r.CrashPoint.ReplyReceived || r.CrashPoint.Outcome != "not-applied" || r.CrashPoint.Passed {
		t.Fatalf("an acknowledged write that vanished is a violation: %+v crash point %+v", r, r.CrashPoint)
	}
}

func TestCrashMidRequestNeedsAService(t *testing.T) {
	c := scenario("http://127.0.0.1:1", "crash-mid-request", nil)
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "requires a managed service") {
		t.Fatalf("got %v", err)
	}
	c = scenario("http://127.0.0.1:1", "lost-response", nil)
	c.CrashDelay = "5ms"
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "crash_delay only applies") {
		t.Fatalf("got %v", err)
	}
	c.Scenario, c.CrashDelay = "crash-mid-request", "1h"
	c.Service = &config.Service{Command: []string{"svc"}, ReadyURL: "http://127.0.0.1:1/health"}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "crash_delay must be") {
		t.Fatalf("got %v", err)
	}
}
