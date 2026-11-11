package fixture

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type entry struct {
	Key         string `json:"key"`
	Fingerprint string `json:"fingerprint,omitempty"`
}

// slowStep is how long the two crash-window bug modes pause between their
// two writes. It is long enough for a kill to land in between every time.
const slowStep = 150 * time.Millisecond

type store struct {
	mu      sync.Mutex
	journal string
	keys    map[string]string
	count   int64
	order   []string
	// claims and charges exist for the bug modes that split one operation
	// into two separate writes. A correct service has no need for either.
	claims  map[string]string
	charges int64
}

func openStore(path string) (*store, error) {
	s := &store{journal: path, keys: map[string]string{}, claims: map[string]string{}}
	lines, err := readLines(path)
	if err != nil {
		return nil, err
	}
	for _, line := range lines {
		var e entry
		if err := json.Unmarshal(line, &e); err != nil {
			return nil, fmt.Errorf("read journal: %w", err)
		}
		s.count++
		s.keys[e.Key] = e.Fingerprint
		s.order = append(s.order, e.Key)
	}
	claims, err := readLines(path + ".keys")
	if err != nil {
		return nil, err
	}
	for _, line := range claims {
		var e entry
		if err := json.Unmarshal(line, &e); err != nil {
			return nil, fmt.Errorf("read claimed keys: %w", err)
		}
		s.claims[e.Key] = e.Fingerprint
	}
	charges, err := readLines(path + ".charges")
	s.charges = int64(len(charges))
	return s, err
}

// readLines returns the complete lines of an append-only file. A crash can
// cut the last line short; like any write-ahead log, the fixture drops that
// tail on startup instead of refusing to start.
func readLines(path string) ([][]byte, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	whole := bytes.LastIndexByte(data, '\n') + 1
	if whole < len(data) {
		if err := os.Truncate(path, int64(whole)); err != nil {
			return nil, err
		}
	}
	var lines [][]byte
	for _, line := range bytes.Split(data[:whole], []byte{'\n'}) {
		if len(line) > 0 {
			lines = append(lines, line)
		}
	}
	return lines, nil
}

// appendLine writes one line and waits for it to reach the disk.
func appendLine(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	line, _ := json.Marshal(value)
	line = append(line, '\n')
	count, err := f.Write(line)
	if err == nil && count != len(line) {
		err = fmt.Errorf("short journal write")
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}

func (s *store) add(key string, deduplicate bool) (bool, error) {
	added, _, err := s.addWithFingerprint(key, "", deduplicate)
	return added, err
}

func (s *store) addWithFingerprint(key, fingerprint string, deduplicate bool) (bool, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if previous, exists := s.keys[key]; deduplicate && exists {
		return false, previous, nil
	}
	if err := appendLine(s.journal, entry{Key: key, Fingerprint: fingerprint}); err != nil {
		return false, "", err
	}
	s.count++
	s.keys[key] = fingerprint
	s.order = append(s.order, key)
	return true, "", nil
}

// claim records a key on its own, before any effect exists.
func (s *store) claim(key, fingerprint string) (bool, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if previous, exists := s.claims[key]; exists {
		return false, previous, nil
	}
	if err := appendLine(s.journal+".keys", entry{Key: key, Fingerprint: fingerprint}); err != nil {
		return false, "", err
	}
	s.claims[key] = fingerprint
	return true, "", nil
}

// charge records the second effect of an operation in a file of its own.
func (s *store) charge(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := appendLine(s.journal+".charges", entry{Key: key}); err != nil {
		return err
	}
	s.charges++
	return nil
}

// state reports both effects. Every mode but torn-write-bug derives the
// charge from the operation's own journal line, so the two can't disagree.
func (s *store) state(separateCharges bool) map[string]int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	charges := s.count
	if separateCharges {
		charges = s.charges
	}
	return map[string]int64{"count": s.count, "charges": charges}
}

// list returns one item per journal entry, so a duplicate write shows up as
// a longer list, just like a real collection endpoint.
func (s *store) list() []map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]map[string]string, len(s.order))
	for i, key := range s.order {
		items[i] = map[string]string{"operation_id": key}
	}
	return items
}

func (s *store) has(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, exists := s.keys[key]
	return exists
}

// Modes lists what the fixture can be: one correct service, and one service
// per deliberate bug.
var Modes = []string{"correct", "duplicate-bug", "race-bug", "early-ack-bug", "replay-bug", "repair-after-crash-bug", "torn-write-bug", "claim-first-bug"}

// New returns a tiny persistent HTTP service used for executable demonstrations.
// The bug modes are intentional and must never be used as production examples.
func New(mode, journal string) (http.Handler, error) {
	switch mode {
	case "correct", "duplicate-bug", "race-bug", "early-ack-bug", "replay-bug", "repair-after-crash-bug", "torn-write-bug", "claim-first-bug":
	default:
		return nil, fmt.Errorf("unknown fixture mode %q; choose one of %s", mode, strings.Join(Modes, ", "))
	}
	s, err := openStore(journal)
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/state", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(s.state(mode == "torn-write-bug"))
	})
	mux.HandleFunc("/openapi.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(openAPI))
	})
	mux.HandleFunc("/operations", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(s.list())
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		key := r.Header.Get("Idempotency-Key")
		if key == "" {
			http.Error(w, "Idempotency-Key required", http.StatusBadRequest)
			return
		}
		writeOperation := func(status int, id string) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]string{"operation_id": id})
		}
		if mode == "correct" || mode == "torn-write-bug" || mode == "claim-first-bug" {
			body, err := io.ReadAll(io.LimitReader(r.Body, (1<<20)+1))
			if err != nil {
				http.Error(w, "request body failed", http.StatusBadRequest)
				return
			}
			if len(body) > 1<<20 {
				http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
				return
			}
			digest := sha256.Sum256(body)
			fingerprint := hex.EncodeToString(digest[:])
			var added bool
			var previous string
			if mode == "claim-first-bug" {
				// Bug: the key is saved before the work it stands for. A crash
				// in between leaves a key that claims work nobody did.
				added, previous, err = s.claim(key, fingerprint)
			} else {
				added, previous, err = s.addWithFingerprint(key, fingerprint, true)
			}
			if err != nil {
				http.Error(w, "journal write failed", http.StatusInternalServerError)
				return
			}
			if !added {
				if previous != "" && previous != fingerprint {
					http.Error(w, "Idempotency-Key used with a different body", http.StatusUnprocessableEntity)
					return
				}
				writeOperation(http.StatusOK, key)
				return
			}
			switch mode {
			case "claim-first-bug":
				time.Sleep(slowStep)
				_, _, err = s.addWithFingerprint(key, fingerprint, false)
			case "torn-write-bug":
				// Bug: the operation and its charge are two separate writes. A
				// crash in between leaves an operation that was never charged.
				time.Sleep(slowStep)
				err = s.charge(key)
			}
			if err != nil {
				http.Error(w, "journal write failed", http.StatusInternalServerError)
				return
			}
			writeOperation(http.StatusCreated, key)
			return
		}
		if mode == "repair-after-crash-bug" {
			marker := journal + ".ack"
			if _, err := os.Stat(marker); errors.Is(err, os.ErrNotExist) {
				if err := os.MkdirAll(filepath.Dir(marker), 0755); err != nil {
					http.Error(w, "marker directory failed", http.StatusInternalServerError)
					return
				}
				if err := os.WriteFile(marker, []byte("acknowledged"), 0600); err != nil {
					http.Error(w, "marker write failed", http.StatusInternalServerError)
					return
				}
				writeOperation(http.StatusCreated, key)
				return
			} else if err != nil {
				http.Error(w, "marker read failed", http.StatusInternalServerError)
				return
			}
		}
		if mode == "early-ack-bug" {
			go func() {
				time.Sleep(5 * time.Second)
				_, _ = s.add(key, true)
			}()
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte("accepted before durable write"))
			return
		}
		if mode == "race-bug" {
			if s.has(key) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("already applied"))
				return
			}
			// Deliberately split the check and write to expose a race under overlap.
			time.Sleep(100 * time.Millisecond)
			if _, err := s.add(key, false); err != nil {
				http.Error(w, "journal write failed", http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte("applied"))
			return
		}
		added, err := s.add(key, mode == "replay-bug" || mode == "repair-after-crash-bug")
		if err != nil {
			http.Error(w, "journal write failed", http.StatusInternalServerError)
			return
		}
		if !added {
			if mode == "replay-bug" || mode == "repair-after-crash-bug" {
				id := key
				if mode == "replay-bug" {
					id += "-changed"
				}
				writeOperation(http.StatusOK, id)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("already applied"))
			return
		}
		if mode == "replay-bug" || mode == "repair-after-crash-bug" {
			writeOperation(http.StatusCreated, key)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("applied"))
	})
	return mux, nil
}

// openAPI describes the fixture the way FastAPI or springdoc would describe a
// real service, so "recoverylab discover" has something to read in the demo.
const openAPI = `{
  "openapi": "3.0.3",
  "info": { "title": "RecoveryLab fixture", "version": "1" },
  "paths": {
    "/operations": {
      "get": {
        "summary": "List operations",
        "responses": { "200": { "description": "Every recorded operation", "content": { "application/json": { "schema": { "type": "array", "items": { "$ref": "#/components/schemas/Operation" } } } } } }
      },
      "post": {
        "summary": "Record an operation",
        "parameters": [ { "name": "Idempotency-Key", "in": "header", "required": true, "schema": { "type": "string" } } ],
        "requestBody": { "required": true, "content": { "application/json": { "schema": { "$ref": "#/components/schemas/NewOperation" } } } },
        "responses": { "201": { "description": "Recorded", "content": { "application/json": { "schema": { "$ref": "#/components/schemas/Operation" } } } } }
      }
    },
    "/state": {
      "get": { "responses": { "200": { "description": "How many operations and charges exist", "content": { "application/json": { "schema": { "type": "object", "properties": { "count": { "type": "integer" }, "charges": { "type": "integer" } } } } } } } }
    }
  },
  "components": {
    "schemas": {
      "NewOperation": { "type": "object", "required": ["amount"], "properties": { "amount": { "type": "integer", "minimum": 1 }, "note": { "type": "string" } } },
      "Operation": { "type": "object", "properties": { "operation_id": { "type": "string" } } }
    }
  }
}`
