package engine

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/Wali05/recoverylab/internal/config"
	"github.com/Wali05/recoverylab/internal/strictjson"
)

func checkRetryResponse(rule *config.RetryResponse, originalStatus int, originalBody []byte, retryStatus int, retryBody []byte) (ResponseCheck, error) {
	check := ResponseCheck{OriginalHTTPStatus: originalStatus, RetryHTTPStatus: retryStatus, ExpectedStatus: "2xx"}
	allowed := retryStatus >= 200 && retryStatus < 300
	if rule != nil {
		check.SameJSONPointers = append([]string(nil), rule.SameJSONPointers...)
		if len(rule.AllowedStatuses) > 0 {
			allowed = false
			labels := make([]string, len(rule.AllowedStatuses))
			for i, status := range rule.AllowedStatuses {
				labels[i] = strconv.Itoa(status)
				if retryStatus == status {
					allowed = true
				}
			}
			check.ExpectedStatus = strings.Join(labels, ", ")
		}
	}
	if !allowed {
		check.Issues = append(check.Issues, fmt.Sprintf("retry returned HTTP %d; expected %s", retryStatus, check.ExpectedStatus))
	}
	if rule != nil && rule.CompareIDs && len(check.SameJSONPointers) == 0 {
		// Only a successful retry is expected to repeat the IDs: a declared
		// 409 for duplicates naturally carries an error body instead.
		check.SameJSONPointers = idPointers(originalBody)
		check.AutoPointers = len(check.SameJSONPointers) > 0
		if retryStatus < 200 || retryStatus >= 300 {
			check.SameJSONPointers, check.AutoPointers = nil, false
		}
	}
	if len(check.SameJSONPointers) > 0 {
		original, err := responseJSON(originalBody)
		if err != nil {
			return check, fmt.Errorf("original response is not usable JSON: %w", err)
		}
		for _, pointer := range check.SameJSONPointers {
			if _, err := atPointer(original, pointer); err != nil {
				return check, fmt.Errorf("original response: %w", err)
			}
		}
		if allowed {
			retry, err := responseJSON(retryBody)
			if err != nil {
				check.Issues = append(check.Issues, fmt.Sprintf("retry response is not usable JSON: %v", err))
			} else {
				for _, pointer := range check.SameJSONPointers {
					before, _ := atPointer(original, pointer)
					after, err := atPointer(retry, pointer)
					if err != nil || !reflect.DeepEqual(before, after) {
						check.Issues = append(check.Issues, fmt.Sprintf("retry JSON value at %q differs from original or is missing", pointer))
					}
				}
			}
		}
	}
	check.Passed = len(check.Issues) == 0
	return check, nil
}

// idPointers finds fields in a reply that name the thing that was created:
// "id", "uuid", and keys ending in "_id" or "Id", at the top level or one
// object down (as in {"data": {"id": ...}}). Fields that are meant to differ
// on every request, such as request_id or traceId, are left out.
func idPointers(body []byte) []string {
	root, err := responseJSON(body)
	if err != nil {
		return nil
	}
	var out []string
	var walk func(node any, prefix string, depth int)
	walk = func(node any, prefix string, depth int) {
		m, ok := node.(map[string]any)
		if !ok || depth > 1 {
			return
		}
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			pointer := prefix + "/" + strings.ReplaceAll(strings.ReplaceAll(k, "~", "~0"), "/", "~1")
			switch m[k].(type) {
			case string, json.Number:
				if isIDField(k) && len(out) < 8 {
					out = append(out, pointer)
				}
			case map[string]any:
				walk(m[k], pointer, depth+1)
			}
		}
	}
	walk(root, "", 0)
	return out
}

func isIDField(name string) bool {
	lower := strings.ToLower(name)
	for _, perRequest := range []string{"request", "trace", "span", "correlation", "session", "idempotency", "nonce", "etag"} {
		if strings.Contains(lower, perRequest) {
			return false
		}
	}
	return lower == "id" || lower == "uuid" || strings.HasSuffix(lower, "_id") || strings.HasSuffix(name, "Id") || strings.HasSuffix(name, "ID")
}

func responseJSON(data []byte) (any, error) {
	if err := strictjson.RejectDuplicateKeys(data); err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var root any
	if err := dec.Decode(&root); err != nil {
		return nil, err
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		if err == nil {
			return nil, errors.New("multiple JSON values")
		}
		return nil, fmt.Errorf("trailing JSON: %w", err)
	}
	return root, nil
}
