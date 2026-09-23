package engine

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"recoverylab/internal/config"
	"recoverylab/internal/fixture"
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
			r := Run(context.Background(), scenario(base, "crash-after-ack", service))
			if r.Status != item.want || !r.FaultInjected || r.Observed == nil || *r.Observed != item.final {
				t.Fatalf("unexpected report: %+v", r)
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
}
