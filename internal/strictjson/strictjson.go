package strictjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// RejectDuplicateKeys refuses ambiguous object members anywhere in a JSON value.
func RejectDuplicateKeys(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	var scan func() error
	scan = func() error {
		token, err := dec.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for dec.More() {
				keyToken, err := dec.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return errors.New("object member name is not a string")
				}
				if seen[key] {
					return fmt.Errorf("duplicate object member %q", key)
				}
				seen[key] = true
				if err := scan(); err != nil {
					return err
				}
			}
			_, err := dec.Token()
			return err
		case '[':
			for dec.More() {
				if err := scan(); err != nil {
					return err
				}
			}
			_, err := dec.Token()
			return err
		default:
			return errors.New("unexpected closing delimiter")
		}
	}
	return scan()
}
