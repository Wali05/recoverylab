package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/Wali05/recoverylab/internal/config"
)

func TestRequestsCarryHostAndAJSONContentType(t *testing.T) {
	var mu sync.Mutex
	count := 0
	var writeHosts, readHosts, contentTypes []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method == http.MethodPost {
			writeHosts = append(writeHosts, r.Host)
			contentTypes = append(contentTypes, r.Header.Get("Content-Type"))
			if count == 0 {
				count++
			}
			w.WriteHeader(http.StatusCreated)
			return
		}
		readHosts = append(readHosts, r.Host)
		_ = json.NewEncoder(w).Encode(map[string]int{"count": count})
	}))
	defer server.Close()
	delta := int64(1)
	r := Run(context.Background(), config.Config{
		Name: "virtual-host", Scenario: "lost-response", Timeout: "5s",
		Request: config.Request{Method: "POST", URL: server.URL + "/orders", Headers: map[string]string{"host": "tenant.example"}, Body: json.RawMessage(`{"sku":"book"}`)},
		Observe: config.Observation{URL: server.URL + "/state", Pointer: "/count", ExpectedDelta: &delta, Headers: map[string]string{"Host": "tenant.example"}},
	})
	if r.Status != "PASS" {
		t.Fatalf("unexpected report: %+v", r)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, host := range append(writeHosts, readHosts...) {
		if host != "tenant.example" {
			t.Fatalf("Host header was ignored: writes %q, reads %q", writeHosts, readHosts)
		}
	}
	if len(writeHosts) != 2 || len(readHosts) != 2 {
		t.Fatalf("expected two writes and two reads: %q %q", writeHosts, readHosts)
	}
	for _, ct := range contentTypes {
		if ct != "application/json" {
			t.Fatalf("a JSON body went out as %q", ct)
		}
	}
}

func TestDeclaredContentTypeIsKept(t *testing.T) {
	var got string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Content-Type")
	}))
	defer server.Close()
	req := config.Request{Method: "POST", URL: server.URL, Headers: map[string]string{"content-type": "application/merge-patch+json"}, Body: json.RawMessage(`{}`)}
	if _, err := send(context.Background(), req, req.URL); err != nil {
		t.Fatal(err)
	}
	if got != "application/merge-patch+json" {
		t.Fatalf("content type was replaced with %q", got)
	}
}
