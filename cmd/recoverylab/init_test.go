package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Wali05/recoverylab/internal/config"
)

func TestInitReadsStateWithATokenFromTheEnvironment(t *testing.T) {
	var hosts []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer s3cret" {
			http.Error(w, "who are you?", http.StatusUnauthorized)
			return
		}
		hosts = append(hosts, r.Host)
		_, _ = w.Write([]byte(`{"items":[],"account_number":4411,"phone_number":5550100}`))
	}))
	defer server.Close()
	out := filepath.Join(t.TempDir(), "scenario.json")
	args := []string{"init", "--url", server.URL + "/orders", "--state", server.URL + "/orders",
		"--header", "Authorization: Bearer {{env:RECOVERYLAB_INIT_TOKEN}}", "--header", "Host: tenant.example", "--out", out}

	if code := run(args); code != 2 {
		t.Fatalf("init without the variable exited %d, want 2", code)
	}
	t.Setenv("RECOVERYLAB_INIT_TOKEN", "s3cret")
	if code := run(args); code != 0 {
		t.Fatalf("init exited %d", code)
	}
	if len(hosts) != 1 || hosts[0] != "tenant.example" {
		t.Fatalf("Host header wasn't sent: %q", hosts)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "s3cret") || !strings.Contains(string(data), "{{env:RECOVERYLAB_INIT_TOKEN}}") {
		t.Fatalf("the file should keep the placeholder, not the token:\n%s", data)
	}
	c, err := config.Load(out)
	if err != nil {
		t.Fatal(err)
	}
	// A list beats a number that merely has "num" in its name.
	if c.Observe.Pointer != "/items" || c.Observe.Measure != config.MeasureLength {
		t.Fatalf("counted %q (%s), want the /items list", c.Observe.Pointer, c.Observe.Measure)
	}
}

func TestCounterRankReadsWholeWords(t *testing.T) {
	for name, want := range map[string]int{
		"total": 5, "totalElements": 5, "total_count": 5, "TotalItems": 5,
		"count": 4, "order_count": 4, "orderCount": 4, "num_orders": 4, "queue-size": 4, "numberOfElements": 4,
		"account_number": 1, "phone_number": 1, "amount": 1, "size": 1, "number": 1, "enumeration": 1, "discount": 1,
		"page": 0, "totalPages": 0, "total_pages": 0, "page_size": 0, "pageSize": 0, "per_page": 0, "limit": 0, "version": 0, "id": 0,
	} {
		if got := counterRank(name); got != want {
			t.Errorf("counterRank(%q) = %d, want %d (words %q)", name, got, want, nameWords(name))
		}
	}
	if got := strings.Join(nameWords("HTTPStatusCode_v2-final"), " "); got != "http status code v2 final" {
		t.Errorf("nameWords: %q", got)
	}
}

func TestSpringPageCountsTheTotal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"content":[{"id":1}],"number":0,"numberOfElements":1,"size":20,"totalElements":1,"totalPages":1}`))
	}))
	defer server.Close()
	out := filepath.Join(t.TempDir(), "scenario.json")
	if code := run([]string{"init", "--url", server.URL + "/orders", "--state", server.URL + "/orders", "--out", out}); code != 0 {
		t.Fatalf("init exited %d", code)
	}
	c, err := config.Load(out)
	if err != nil {
		t.Fatal(err)
	}
	if c.Observe.Pointer != "/totalElements" {
		t.Fatalf("counted %q, want /totalElements", c.Observe.Pointer)
	}
}

func TestHelpIsNotAnError(t *testing.T) {
	for _, args := range [][]string{{"help"}, {"--help"}, {"audit", "--help"}, {"run", "-h"}, {"init", "--help"}, {"discover", "--help"}, {"fixture", "--help"}} {
		if code := run(args); code != 0 {
			t.Errorf("%v exited %d, want 0", args, code)
		}
	}
	if code := run([]string{"nonsense"}); code != 2 {
		t.Errorf("unknown command exited %d, want 2", code)
	}
}

func TestWrapTextCountsCharactersNotBytes(t *testing.T) {
	// Each word is 4 characters but 8 or more bytes.
	if got := wrapText("→→→→ ……… éééé", 9, 0); got != "→→→→ ………\néééé" {
		t.Fatalf("got %q", got)
	}
}

func TestDiscoverKeepsRequestsOnThisMachine(t *testing.T) {
	out := filepath.Join(t.TempDir(), "scenarios")
	if code := run([]string{"discover", "--openapi", "http://example.com/openapi.json", "--out", out}); code != 2 {
		t.Fatalf("a remote --openapi URL exited %d, want 2", code)
	}
	if code := run([]string{"discover", "--openapi", "http://example.com/openapi.json", "--base", "http://127.0.0.1:1", "--out", out}); code != 2 {
		t.Fatalf("a remote --openapi URL with a local base exited %d, want 2", code)
	}
	spec := filepath.Join(t.TempDir(), "openapi.json")
	if err := os.WriteFile(spec, []byte(`{"openapi":"3.0.3","paths":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"discover", "--openapi", spec, "--out", out}); code != 2 {
		t.Fatalf("a file without --base exited %d, want 2", code)
	}
}
