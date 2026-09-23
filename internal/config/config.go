package config

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Config describes one reproducible failure experiment.
type Config struct {
	Name        string      `json:"name"`
	Scenario    string      `json:"scenario"`
	Timeout     string      `json:"timeout,omitempty"`
	Concurrency int         `json:"concurrency,omitempty"`
	Service     *Service    `json:"service,omitempty"`
	Request     Request     `json:"request"`
	Observe     Observation `json:"observe"`
	SourceDir   string      `json:"-"`
}

type Service struct {
	Command        []string `json:"command"`
	WorkingDir     string   `json:"working_dir,omitempty"`
	ReadyURL       string   `json:"ready_url"`
	StartupTimeout string   `json:"startup_timeout,omitempty"`
}

type Request struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    json.RawMessage   `json:"body,omitempty"`
}

type Observation struct {
	URL           string `json:"url"`
	Pointer       string `json:"pointer"`
	ExpectedDelta *int64 `json:"expected_delta"`
}

func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var c Config
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if dec.Decode(new(any)) != io.EOF {
		return Config{}, errors.New("scenario file must contain exactly one JSON object")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return Config{}, err
	}
	c.SourceDir = filepath.Dir(abs)
	if c.Name == "" {
		c.Name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	return c, c.Validate()
}

func NewRunID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func (c *Config) Resolve(runID string) {
	replace := func(s string) string { return strings.ReplaceAll(s, "{{run_id}}", runID) }
	c.Name = replace(c.Name)
	c.Request.URL = replace(c.Request.URL)
	for k, v := range c.Request.Headers {
		c.Request.Headers[k] = replace(v)
	}
	c.Request.Body = json.RawMessage(replace(string(c.Request.Body)))
	c.Observe.URL = replace(c.Observe.URL)
}

func (c Config) Duration() time.Duration {
	if c.Timeout == "" {
		return 20 * time.Second
	}
	d, _ := time.ParseDuration(c.Timeout)
	return d
}

func (c Config) Workers() int {
	if c.Concurrency == 0 {
		return 16
	}
	return c.Concurrency
}

func (s Service) StartupDuration() time.Duration {
	if s.StartupTimeout == "" {
		return 10 * time.Second
	}
	d, _ := time.ParseDuration(s.StartupTimeout)
	return d
}

func (c Config) Validate() error {
	if c.Name == "" {
		return errors.New("name is required")
	}
	if c.Scenario != "lost-response" && c.Scenario != "crash-after-ack" && c.Scenario != "concurrent-duplicates" {
		return errors.New("scenario must be lost-response, concurrent-duplicates, or crash-after-ack")
	}
	if c.Scenario == "concurrent-duplicates" {
		if c.Workers() < 2 || c.Workers() > 64 {
			return errors.New("concurrency must be between 2 and 64")
		}
	} else if c.Concurrency != 0 {
		return errors.New("concurrency only applies to concurrent-duplicates")
	}
	if c.Timeout != "" {
		d, err := time.ParseDuration(c.Timeout)
		if err != nil || d <= 0 {
			return errors.New("timeout must be a positive duration, such as 20s")
		}
	}
	if c.Request.Method == "" {
		return errors.New("request.method is required")
	}
	switch strings.ToUpper(c.Request.Method) {
	case "POST", "PUT", "PATCH", "DELETE":
	default:
		return errors.New("request.method must be POST, PUT, PATCH, or DELETE")
	}
	if err := localHTTPURL(c.Request.URL); err != nil {
		return fmt.Errorf("request.url: %w", err)
	}
	if err := localHTTPURL(c.Observe.URL); err != nil {
		return fmt.Errorf("observe.url: %w", err)
	}
	if c.Observe.ExpectedDelta == nil {
		return errors.New("observe.expected_delta is required")
	}
	if c.Observe.Pointer != "" && !strings.HasPrefix(c.Observe.Pointer, "/") {
		return errors.New("observe.pointer must be an RFC 6901 JSON pointer, such as /count")
	}
	if len(c.Request.Body) > 0 && !json.Valid(c.Request.Body) {
		return errors.New("request.body must be valid JSON")
	}
	if len(c.Request.Body) > 1<<20 {
		return errors.New("request.body must be at most 1 MiB")
	}
	for k, v := range c.Request.Headers {
		if strings.ContainsAny(k, "\r\n") || strings.ContainsAny(v, "\r\n") {
			return errors.New("request.headers cannot contain line breaks")
		}
	}
	if c.Scenario == "crash-after-ack" && c.Service == nil {
		return errors.New("crash-after-ack requires a managed service")
	}
	if c.Service != nil {
		if len(c.Service.Command) == 0 || c.Service.Command[0] == "" {
			return errors.New("service.command requires an executable and optional arguments")
		}
		if err := localHTTPURL(c.Service.ReadyURL); err != nil {
			return fmt.Errorf("service.ready_url: %w", err)
		}
		if c.Service.StartupTimeout != "" {
			d, err := time.ParseDuration(c.Service.StartupTimeout)
			if err != nil || d <= 0 {
				return errors.New("service.startup_timeout must be a positive duration")
			}
		}
	}
	return nil
}

func localHTTPURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u == nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return errors.New("must be an absolute HTTP URL")
	}
	host := u.Hostname()
	if !strings.EqualFold(host, "localhost") && (net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback()) {
		return errors.New("only loopback targets are supported (localhost, 127.0.0.1, or ::1)")
	}
	return nil
}
