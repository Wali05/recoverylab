package engine

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"

	"github.com/Wali05/recoverylab/internal/config"
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

func responseJSON(data []byte) (any, error) {
	if err := rejectDuplicateKeys(data); err != nil {
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
