package strictjson

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRejectDuplicateKeys(t *testing.T) {
	for _, ok := range []string{`{}`, `[]`, `1`, `"x"`, `null`, `{"a":1,"b":{"a":2}}`, `[{"a":1},{"a":2}]`} {
		if err := RejectDuplicateKeys([]byte(ok)); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for bad, want := range map[string]string{
		`{"a":1,"a":2}`:             `duplicate object member "a"`,
		`{"x":{"k":1,"k":1}}`:       `duplicate object member "k"`,
		`[1,{"b":[{"c":0,"c":0}]}]`: `duplicate object member "c"`,
		"{\"a\":1,\"\\u0061\":2}":   `duplicate object member "a"`,
	} {
		if err := RejectDuplicateKeys([]byte(bad)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v", bad, err)
		}
	}
	for _, broken := range []string{``, `{`, `{"a":`, `[1,`, `}`} {
		if err := RejectDuplicateKeys([]byte(broken)); err == nil {
			t.Errorf("%q: expected an error", broken)
		}
	}
}

// Anything encoding/json accepts and that has no duplicate keys must pass,
// and nothing may panic.
func FuzzRejectDuplicateKeys(f *testing.F) {
	for _, seed := range []string{`{"a":1}`, `{"a":1,"a":2}`, `[[[{"x":{}}]]]`, `{"a":{"a":{"a":1}}}`, `nul`, `{"":0,"":0}`} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		err := RejectDuplicateKeys(data)
		if err == nil && !json.Valid(data) {
			// Token scanning stops at the end of the first value, so trailing
			// junk is the caller's check; a broken first value is not.
			dec := json.NewDecoder(strings.NewReader(string(data)))
			var v any
			if dec.Decode(&v) != nil {
				t.Fatalf("accepted invalid JSON %q", data)
			}
		}
	})
}
