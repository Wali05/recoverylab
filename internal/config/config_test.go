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
