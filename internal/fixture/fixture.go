package fixture

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type entry struct {
	Key string `json:"key"`
}

type store struct {
	mu      sync.Mutex
	journal string
	keys    map[string]bool
	count   int64
}

func openStore(path string) (*store, error) {
	s := &store{journal: path, keys: map[string]bool{}}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var e entry
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			return nil, fmt.Errorf("read journal: %w", err)
		}
		s.count++
		s.keys[e.Key] = true
	}
	return s, scanner.Err()
}

func (s *store) add(key string, deduplicate bool) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if deduplicate && s.keys[key] {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(s.journal), 0755); err != nil {
		return false, err
	}
	f, err := os.OpenFile(s.journal, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return false, err
	}
	line, _ := json.Marshal(entry{Key: key})
	line = append(line, '\n')
	count, err := f.Write(line)
	if err == nil && count != len(line) {
		err = fmt.Errorf("short journal write")
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return false, err
	}
	if closeErr != nil {
		return false, closeErr
	}
	s.count++
	s.keys[key] = true
	return true, nil
}

func (s *store) value() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.count
}

func (s *store) has(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.keys[key]
}

// New returns a tiny persistent HTTP service used for executable demonstrations.
// The bug modes are intentional and must never be used as production examples.
func New(mode, journal string) (http.Handler, error) {
	switch mode {
	case "correct", "duplicate-bug", "race-bug", "early-ack-bug", "replay-bug", "repair-after-crash-bug":
	default:
		return nil, fmt.Errorf("unknown fixture mode %q", mode)
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
		_ = json.NewEncoder(w).Encode(map[string]int64{"count": s.value()})
	})
	mux.HandleFunc("/operations", func(w http.ResponseWriter, r *http.Request) {
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
		added, err := s.add(key, mode == "correct" || mode == "replay-bug" || mode == "repair-after-crash-bug")
		if err != nil {
			http.Error(w, "journal write failed", http.StatusInternalServerError)
			return
		}
		if !added {
			if mode == "correct" || mode == "replay-bug" || mode == "repair-after-crash-bug" {
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
		if mode == "correct" || mode == "replay-bug" || mode == "repair-after-crash-bug" {
			writeOperation(http.StatusCreated, key)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("applied"))
	})
	return mux, nil
}
