package fixture

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func post(t *testing.T, h http.Handler, key, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/operations", strings.NewReader(body))
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func state(t *testing.T, h http.Handler) map[string]int64 {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/state", nil))
	var out map[string]int64
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestCorrectModeIsIdempotentAndDurable(t *testing.T) {
	journal := filepath.Join(t.TempDir(), "journal.jsonl")
	h, err := New("correct", journal)
	if err != nil {
		t.Fatal(err)
	}
	if rec := post(t, h, "", `{}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("no key: %d", rec.Code)
	}
	if rec := post(t, h, "k1", `{"amount":1}`); rec.Code != http.StatusCreated {
		t.Fatalf("first: %d", rec.Code)
	}
	if rec := post(t, h, "k1", `{"amount":1}`); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"k1"`) {
		t.Fatalf("replay: %d %s", rec.Code, rec.Body)
	}
	if rec := post(t, h, "k1", `{"amount":2}`); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("changed body: %d", rec.Code)
	}
	if s := state(t, h); s["count"] != 1 || s["charges"] != 1 {
		t.Fatalf("state: %v", s)
	}
	// A new process reading the same journal remembers everything.
	again, err := New("correct", journal)
	if err != nil {
		t.Fatal(err)
	}
	if rec := post(t, again, "k1", `{"amount":1}`); rec.Code != http.StatusOK {
		t.Fatalf("replay after restart: %d", rec.Code)
	}
	if s := state(t, again); s["count"] != 1 {
		t.Fatalf("state after restart: %v", s)
	}
}

func TestStartupDropsALineCutShortByACrash(t *testing.T) {
	journal := filepath.Join(t.TempDir(), "journal.jsonl")
	if err := os.WriteFile(journal, []byte("{\"key\":\"whole\"}\n{\"key\":\"to"), 0600); err != nil {
		t.Fatal(err)
	}
	h, err := New("correct", journal)
	if err != nil {
		t.Fatalf("a torn tail must not stop the service: %v", err)
	}
	if s := state(t, h); s["count"] != 1 {
		t.Fatalf("state: %v", s)
	}
	if rec := post(t, h, "next", `{}`); rec.Code != http.StatusCreated {
		t.Fatalf("write after recovery: %d", rec.Code)
	}
	data, _ := os.ReadFile(journal)
	if lines := strings.Split(strings.TrimSpace(string(data)), "\n"); len(lines) != 2 || !strings.Contains(lines[1], "next") {
		t.Fatalf("journal after recovery:\n%s", data)
	}
	// Damage in the middle is not a torn tail and must be reported.
	if err := os.WriteFile(journal, []byte("garbage\n{\"key\":\"ok\"}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := New("correct", journal); err == nil {
		t.Fatal("a corrupt journal line was accepted")
	}
}

func TestBugModesMisbehaveAsAdvertised(t *testing.T) {
	dir := t.TempDir()
	dup, _ := New("duplicate-bug", filepath.Join(dir, "dup.jsonl"))
	post(t, dup, "k", `{}`)
	post(t, dup, "k", `{}`)
	if s := state(t, dup); s["count"] != 2 {
		t.Fatalf("duplicate-bug should write twice: %v", s)
	}
	replay, _ := New("replay-bug", filepath.Join(dir, "replay.jsonl"))
	first, second := post(t, replay, "k", `{}`), post(t, replay, "k", `{}`)
	if first.Body.String() == second.Body.String() || state(t, replay)["count"] != 1 {
		t.Fatalf("replay-bug should write once and answer differently: %s %s", first.Body, second.Body)
	}
	torn, _ := New("torn-write-bug", filepath.Join(dir, "torn.jsonl"))
	if rec := post(t, torn, "k", `{}`); rec.Code != http.StatusCreated {
		t.Fatalf("torn-write-bug: %d", rec.Code)
	}
	if s := state(t, torn); s["count"] != 1 || s["charges"] != 1 {
		t.Fatalf("without a crash both writes land: %v", s)
	}
	if _, err := New("nonsense", filepath.Join(dir, "x.jsonl")); err == nil || !strings.Contains(err.Error(), "torn-write-bug") {
		t.Fatalf("unknown mode should list the real ones: %v", err)
	}
	for _, mode := range Modes {
		if _, err := New(mode, filepath.Join(dir, mode+".jsonl")); err != nil {
			t.Errorf("%s: %v", mode, err)
		}
	}
}
