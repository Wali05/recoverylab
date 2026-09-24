package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadStrictAndLocal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "case.json")
	good := `{"name":"sample","scenario":"lost-response","request":{"method":"POST","url":"http://127.0.0.1:8080/write","headers":{"Idempotency-Key":"{{run_id}}"}},"observe":{"url":"http://localhost:8080/state","pointer":"/count","expected_delta":1}}`
	if err := os.WriteFile(path, []byte(good), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	c.Resolve("abc123")
	if c.Request.Headers["Idempotency-Key"] != "abc123" {
		t.Fatal("run ID was not substituted")
	}
	c.Observe.Headers = map[string]string{"X-Read-Token": "key-{{run_id}}"}
	c.Resolve("abc123")
	if c.Observe.Headers["X-Read-Token"] != "key-abc123" {
		t.Fatal("run ID was not substituted in observation header")
	}
	for _, test := range []struct{ label, data, want string }{
		{"remote URL", strings.Replace(good, "127.0.0.1:8080/write", "example.com/write", 1), "only loopback"},
		{"unknown field", strings.Replace(good, `"name":"sample"`, `"name":"sample","surprise":true`, 1), "unknown field"},
		{"missing delta", strings.Replace(good, `,"expected_delta":1`, ``, 1), "expected_delta"},
		{"trailing document", good + `{}`, "exactly one"},
	} {
		t.Run(test.label, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(test.data), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := Load(path)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("got %v, want %q", err, test.want)
			}
		})
	}
}

func TestJSONPointerSyntax(t *testing.T) {
	for _, pointer := range []string{"", "/count", "/a~1b/~0key", "/"} {
		if !ValidJSONPointer(pointer) {
			t.Errorf("rejected valid pointer %q", pointer)
		}
	}
	for _, pointer := range []string{"count", "/bad~", "/bad~2", "/bad~~0"} {
		if ValidJSONPointer(pointer) {
			t.Errorf("accepted invalid pointer %q", pointer)
		}
	}
}

func TestMultipleChecksValidation(t *testing.T) {
	delta := int64(1)
	c := Config{
		Name: "multi", Scenario: "lost-response",
		Request: Request{Method: "POST", URL: "http://127.0.0.1:8080/write"},
		Checks: []Observation{
			{Name: "orders", URL: "http://127.0.0.1:8080/state", Pointer: "/orders", ExpectedDelta: &delta},
			{Name: "charges", URL: "http://127.0.0.1:8080/state", Pointer: "/charges", ExpectedDelta: &delta},
		},
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	c.Checks[1].Name = "orders"
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "duplicate check name") {
		t.Fatalf("duplicate name: got %v", err)
	}
	c.Checks[1].Name = "charges"
	c.Observe = c.Checks[0]
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "either observe or checks") {
		t.Fatalf("mixed observation modes: got %v", err)
	}
}

func TestLoadMultipleChecksJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checks.json")
	data := `{"name":"multi","scenario":"lost-response","request":{"method":"POST","url":"http://127.0.0.1:8080/write"},"checks":[{"name":"orders","url":"http://127.0.0.1:8080/state","pointer":"/orders","expected_delta":1},{"name":"charges","url":"http://127.0.0.1:8080/state","pointer":"/charges","expected_delta":1}]}`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil || len(c.Checks) != 2 {
		t.Fatalf("load multiple checks: %+v, %v", c, err)
	}
}

func TestRetryResponseValidation(t *testing.T) {
	delta := int64(1)
	c := Config{
		Name: "response", Scenario: "lost-response",
		Request:       Request{Method: "POST", URL: "http://127.0.0.1:8080/write"},
		Observe:       Observation{URL: "http://127.0.0.1:8080/state", Pointer: "/count", ExpectedDelta: &delta},
		RetryResponse: &RetryResponse{AllowedStatuses: []int{200, 201}, SameJSONPointers: []string{"/order/id"}},
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	crash := c
	crash.Scenario = "crash-after-ack"
	crash.Service = &Service{Command: []string{"service"}, ReadyURL: "http://127.0.0.1:8080/health"}
	if err := crash.Validate(); err != nil {
		t.Fatalf("retry response should also work after restart: %v", err)
	}
	for _, test := range []struct {
		name string
		edit func(*Config)
		want string
	}{
		{"other scenario", func(c *Config) { c.Scenario = "concurrent-duplicates" }, "only applies"},
		{"bad status", func(c *Config) { c.RetryResponse.AllowedStatuses = []int{700} }, "HTTP codes"},
		{"duplicate status", func(c *Config) { c.RetryResponse.AllowedStatuses = []int{200, 200} }, "distinct"},
		{"bad pointer", func(c *Config) { c.RetryResponse.SameJSONPointers = []string{"bad"} }, "JSON pointer"},
		{"duplicate pointer", func(c *Config) { c.RetryResponse.SameJSONPointers = []string{"/id", "/id"} }, "duplicate"},
	} {
		t.Run(test.name, func(t *testing.T) {
			copy := c
			rule := *c.RetryResponse
			copy.RetryResponse = &rule
			test.edit(&copy)
			if err := copy.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("got %v, want %q", err, test.want)
			}
		})
	}
}

func TestConcurrentResponseValidation(t *testing.T) {
	delta := int64(1)
	c := Config{
		Name: "statuses", Scenario: "concurrent-duplicates",
		Request:            Request{Method: "POST", URL: "http://127.0.0.1:8080/write"},
		Observe:            Observation{URL: "http://127.0.0.1:8080/state", Pointer: "/count", ExpectedDelta: &delta},
		ConcurrentResponse: &ConcurrentResponse{AllowedStatuses: []int{201, 409}},
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name     string
		statuses []int
		want     string
	}{
		{"empty", nil, "1 to 16"},
		{"bad code", []int{700}, "HTTP codes"},
		{"duplicate", []int{409, 409}, "distinct"},
	} {
		t.Run(test.name, func(t *testing.T) {
			copy := c
			copy.ConcurrentResponse = &ConcurrentResponse{AllowedStatuses: test.statuses}
			if err := copy.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("got %v, want %q", err, test.want)
			}
		})
	}
	c.Scenario = "lost-response"
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "only applies") {
		t.Fatalf("wrong scenario: got %v", err)
	}
}
