package engine

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/Wali05/recoverylab/internal/config"
	"github.com/Wali05/recoverylab/internal/fixture"
)

func scenario(base, mode string, service *config.Service) config.Config {
	delta := int64(1)
	return config.Config{
		Name: "integration", Scenario: mode, Timeout: "8s", Service: service,
		Request: config.Request{Method: "POST", URL: base + "/operations", Headers: map[string]string{"Idempotency-Key": "test-{{run_id}}"}, Body: json.RawMessage(`{"amount":1}`)},
		Observe: config.Observation{URL: base + "/state", Pointer: "/count", ExpectedDelta: &delta},
	}
}

func TestLostResponseDetectsDuplicateEffect(t *testing.T) {
	for _, item := range []struct {
		mode, want string
		final      int64
	}{{"correct", "PASS", 1}, {"duplicate-bug", "VIOLATION", 2}, {"race-bug", "PASS", 1}} {
		t.Run(item.mode, func(t *testing.T) {
			handler, err := fixture.New(item.mode, filepath.Join(t.TempDir(), "journal.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(handler)
			defer server.Close()
			r := Run(context.Background(), scenario(server.URL, "lost-response", nil))
			if r.Status != item.want || !r.FaultInjected || r.Observed == nil || *r.Observed != item.final {
				t.Fatalf("unexpected report: %+v", r)
			}
			if len(r.Attempts) != 2 || r.Attempts[0].ClientError == "" {
				t.Fatalf("lost response was not observed: %+v", r.Attempts)
			}
		})
	}
}

func TestConcurrentDuplicatesFindsCheckThenWriteRace(t *testing.T) {
	for _, item := range []struct {
		mode, want string
		minimum    int64
	}{{"correct", "PASS", 1}, {"race-bug", "VIOLATION", 2}} {
		t.Run(item.mode, func(t *testing.T) {
			handler, err := fixture.New(item.mode, filepath.Join(t.TempDir(), "journal.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(handler)
			defer server.Close()
			c := scenario(server.URL, "concurrent-duplicates", nil)
			c.Concurrency = 12
			r := Run(context.Background(), c)
			if r.Status != item.want || !r.FaultInjected || r.Observed == nil || *r.Observed < item.minimum || len(r.Attempts) != 12 {
				t.Fatalf("unexpected report: %+v", r)
			}
		})
	}
}

func TestHelperService(t *testing.T) {
	if os.Getenv("RECOVERYLAB_HELPER") != "1" {
		return
	}
	handler, err := fixture.New(os.Getenv("RECOVERYLAB_MODE"), os.Getenv("RECOVERYLAB_JOURNAL"))
	if err != nil {
		os.Exit(3)
	}
	if err := http.ListenAndServe(os.Getenv("RECOVERYLAB_ADDR"), handler); err != nil {
		os.Exit(4)
	}
}

func TestCrashAfterAcknowledgementChecksDurability(t *testing.T) {
	for _, item := range []struct {
		mode, want string
		final      int64
	}{{"correct", "PASS", 1}, {"early-ack-bug", "VIOLATION", 0}} {
		t.Run(item.mode, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			addr := listener.Addr().String()
			_ = listener.Close()
			t.Setenv("RECOVERYLAB_HELPER", "1")
			t.Setenv("RECOVERYLAB_MODE", item.mode)
			t.Setenv("RECOVERYLAB_ADDR", addr)
			t.Setenv("RECOVERYLAB_JOURNAL", filepath.Join(t.TempDir(), "journal.jsonl"))
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			base := "http://" + addr
			service := &config.Service{Command: []string{exe, "-test.run=^TestHelperService$"}, ReadyURL: base + "/health", StartupTimeout: "4s"}
			c := scenario(base, "crash-after-ack", service)
			r := Run(context.Background(), c)
			if r.Status != item.want || !r.FaultInjected || r.Observed == nil || *r.Observed != item.final {
				t.Fatalf("unexpected report: %+v", r)
			}
			if item.mode == "correct" {
				second := Run(context.Background(), c)
				if second.Status != "PASS" || second.RunID == r.RunID || second.Observed == nil || *second.Observed != 2 {
					t.Fatalf("repeated managed-service run: %+v", second)
				}
			}
		})
	}
}

func TestPointer(t *testing.T) {
	root := map[string]any{"a/b": []any{map[string]any{"~key": 7}}}
	v, err := atPointer(root, "/a~1b/0/~0key")
	if err != nil || v != 7 {
		t.Fatalf("got %v, %v", v, err)
	}
	if _, err := atPointer(root, "/missing"); err == nil {
		t.Fatal("missing pointer was accepted")
	}
	for _, pointer := range []string{"/a~1b/00", "/a~1b/+0", "/a~1b/-", "/a~2b"} {
		if _, err := atPointer(root, pointer); err == nil {
			t.Errorf("invalid pointer %q was accepted", pointer)
		}
	}
}

func TestAgainstIndependentHTTPService(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]bool{}
	var count, calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/state":
			if r.Header.Get("X-Read-Token") != "test-token" {
				http.Error(w, "missing read token", http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]int{"count": count})
		case "/operations":
			calls++
			key := r.Header.Get("Idempotency-Key")
			if !seen[key] {
				seen[key] = true
				count++
			}
			w.WriteHeader(http.StatusCreated)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	c := scenario(server.URL, "lost-response", nil)
	c.Observe.Headers = map[string]string{"X-Read-Token": "test-token"}
	first := Run(context.Background(), c)
	second := Run(context.Background(), c)
	if first.Status != "PASS" || second.Status != "PASS" || first.RunID == second.RunID || c.Request.Headers["Idempotency-Key"] != "test-{{run_id}}" {
		t.Fatalf("repeated runs reused a key or mutated the scenario: first=%+v second=%+v", first, second)
	}
	c.Scenario = "concurrent-duplicates"
	c.Concurrency = 12
	third := Run(context.Background(), c)
	if third.Status != "PASS" || !third.FaultInjected {
		t.Fatalf("concurrent run: %+v", third)
	}
	mu.Lock()
	defer mu.Unlock()
	if count != 3 || calls != 16 {
		t.Fatalf("expected one effect per run and exactly 16 HTTP writes, got count=%d calls=%d", count, calls)
	}
}

func TestMultipleChecksCatchHiddenDuplicateCharge(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]bool{}
	var orders, charges int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path == "/state" {
			_ = json.NewEncoder(w).Encode(map[string]int{"orders": orders, "charges": charges})
			return
		}
		key := r.Header.Get("Idempotency-Key")
		if !seen[key] {
			seen[key] = true
			orders++
		}
		charges++ // The service deduplicates orders but not charges.
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()
	delta := int64(1)
	c := scenario(server.URL, "lost-response", nil)
	c.Observe = config.Observation{}
	c.Checks = []config.Observation{
		{Name: "orders", URL: server.URL + "/state", Pointer: "/orders", ExpectedDelta: &delta},
		{Name: "charges", URL: server.URL + "/state", Pointer: "/charges", ExpectedDelta: &delta},
	}
	r := Run(context.Background(), c)
	if r.Status != "VIOLATION" || len(r.Checks) != 2 || r.Checks[0].Observed == nil || *r.Checks[0].Observed != 1 || r.Checks[1].Observed == nil || *r.Checks[1].Observed != 2 {
		t.Fatalf("duplicate charge was not detected: %+v", r)
	}
}

func TestTruncatedAcknowledgementIsInconclusive(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/state" {
			_, _ = w.Write([]byte(`{"count":0}`))
			return
		}
		w.Header().Set("Content-Length", "10")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()
	r := Run(context.Background(), scenario(server.URL, "lost-response", nil))
	if r.Status != "ERROR" || r.FaultInjected {
		t.Fatalf("truncated reply must not count as a completed acknowledgement: %+v", r)
	}
}

func TestObservationRejectsExtraJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"count":0}{"count":1}`))
	}))
	defer server.Close()
	_, err := observe(context.Background(), config.Observation{URL: server.URL, Pointer: "/count"})
	if err == nil {
		t.Fatal("multiple JSON values were accepted as one observation")
	}
}

func TestObservationRejectsDuplicateKeys(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"count":0,"\u0063ount":1}`))
	}))
	defer server.Close()
	_, err := observe(context.Background(), config.Observation{URL: server.URL, Pointer: "/count"})
	if err == nil {
		t.Fatal("duplicate JSON keys were accepted as a reliable observation")
	}
}
