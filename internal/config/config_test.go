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
