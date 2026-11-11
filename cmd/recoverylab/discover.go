package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Wali05/recoverylab/internal/config"
	"github.com/Wali05/recoverylab/internal/openapi"
)

type paramFlags map[string]string

func (p paramFlags) String() string { return "" }

func (p paramFlags) Set(v string) error {
	name, value, ok := strings.Cut(v, "=")
	if !ok || name == "" {
		return errors.New("params look like id=123")
	}
	p[name] = value
	return nil
}

func discover(args []string) int {
	fs := flag.NewFlagSet("discover", flag.ContinueOnError)
	base := fs.String("base", "", "the running service, such as http://127.0.0.1:8000")
	spec := fs.String("openapi", "", "OpenAPI JSON file or URL; found on --base if omitted")
	out := fs.String("out", "recoverylab", "folder for the generated scenarios")
	force := fs.Bool("force", false, "overwrite scenarios that already exist")
	noKey := fs.Bool("no-key", false, "don't add an Idempotency-Key header")
	headers := headerFlags{}
	fs.Var(headers, "header", `header for every request, such as "Authorization: Bearer token" (repeatable)`)
	params := paramFlags{}
	fs.Var(params, "param", "value for a path parameter, such as id=42 (repeatable)")
	if err := fs.Parse(args); err != nil {
		return flagExit(err)
	}
	if fs.NArg() != 0 || (*base == "" && *spec == "") {
		fmt.Fprintln(os.Stderr, "usage: recoverylab discover --base http://127.0.0.1:8000 [--openapi URL|FILE] [--out DIR] [--header 'K: V'] [--param id=42]")
		return 2
	}
	if *base != "" {
		if err := config.LocalURL(*base); err != nil {
			fmt.Fprintln(os.Stderr, "--base:", err)
			return 2
		}
	}
	// Headers usually carry a token, so a document served over HTTP has to
	// be on this machine too. One hosted elsewhere can be saved to a file.
	specURL := strings.HasPrefix(*spec, "http://") || strings.HasPrefix(*spec, "https://")
	if specURL {
		if err := config.LocalURL(*spec); err != nil {
			fmt.Fprintf(os.Stderr, "--openapi: %v; download the document and pass the file instead\n", err)
			return 2
		}
	}
	if *base == "" {
		u, err := url.Parse(*spec)
		if !specURL || err != nil {
			fmt.Fprintln(os.Stderr, "pass --base so discover knows where the service runs")
			return 2
		}
		*base = u.Scheme + "://" + u.Host
	}
	s := newStyle(os.Stdout)
	// Scenario files keep {{env:NAME}}; live reads need the real value.
	live, err := config.ExpandHeaders(headers)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}

	data, source, err := loadSpec(*base, *spec, live)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	*base = strings.TrimRight(*base, "/")
	doc, err := openapi.Parse(data)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", source, err)
		return 2
	}
	endpoints := doc.Writes(params)
	title := doc.Title()
	if title == "" {
		title = source
	}
	fmt.Fprintf(os.Stdout, "\n %s %s\n %s\n\n", s.bold("Discovered"), title, s.dim(fmt.Sprintf("%s · %d write %s", source, len(endpoints), plural(len(endpoints), "endpoint", "endpoints"))))
	if len(endpoints) == 0 {
		fmt.Fprintln(os.Stdout, "  No POST, PUT, or PATCH operations in this document.")
		return 2
	}
	if err := os.MkdirAll(*out, 0755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}

	written := 0
	width := 0
	for _, e := range endpoints {
		width = max(width, len(e.Method)+1+len(e.Path))
	}
	for _, e := range endpoints {
		label := fmt.Sprintf("%-*s", width, e.Method+" "+e.Path)
		if e.Skip != "" {
			fmt.Fprintf(os.Stdout, "  %s %s  %s\n", s.dim("–"), s.dim(label), s.dim(e.Skip))
			continue
		}
		c, note, err := scenarioFor(*base, e, headers, live, *noKey)
		if err != nil {
			fmt.Fprintf(os.Stdout, "  %s %s  %s\n", s.amber("!"), label, err)
			continue
		}
		path := filepath.Join(*out, c.Name+".json")
		if _, err := os.Stat(path); err == nil && !*force {
			fmt.Fprintf(os.Stdout, "  %s %s  %s\n", s.dim("–"), s.dim(label), s.dim(path+" already exists; --force replaces it"))
			continue
		}
		encoded, err := json.MarshalIndent(c, "", "  ")
		if err == nil {
			err = os.WriteFile(path, append(encoded, '\n'), 0644)
		}
		if err != nil {
			fmt.Fprintf(os.Stdout, "  %s %s  %v\n", s.amber("!"), label, err)
			continue
		}
		written++
		fmt.Fprintf(os.Stdout, "  %s %s  %s %s\n", s.green("✓"), label, note, s.dim("→ "+path))
		if e.Secured && len(headers) == 0 {
			fmt.Fprintf(os.Stdout, "    %s\n", s.amber("needs auth in the spec; rerun with --header 'Authorization: Bearer …'"))
		}
	}
	if written == 0 {
		fmt.Fprintln(os.Stdout, "\n  No scenarios written. The reasons are above.")
		return 2
	}
	fmt.Fprintf(os.Stdout, "\n %s %d %s in %s\n", s.bold("Wrote"), written, plural(written, "scenario", "scenarios"), *out)
	fmt.Fprintf(os.Stdout, " %s\n\n Next:\n   recoverylab audit %s\n\n", s.dim("Check the bodies look like a real request before trusting the verdicts."), *out)
	return 0
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// loadSpec reads the document from a file or URL, or tries the paths popular
// frameworks use on the running service.
func loadSpec(base, spec string, headers map[string]string) ([]byte, string, error) {
	const limit = 16 << 20
	if spec != "" && !strings.HasPrefix(spec, "http://") && !strings.HasPrefix(spec, "https://") {
		data, err := os.ReadFile(spec)
		return data, spec, err
	}
	if spec != "" {
		data, err := fetch(spec, headers, limit)
		if err != nil {
			return nil, "", fmt.Errorf("couldn't read %s: %v", spec, err)
		}
		return data, spec, nil
	}
	base = strings.TrimRight(base, "/")
	for _, p := range openapi.CommonPaths {
		if data, err := fetch(base+p, headers, limit); err == nil {
			if _, err := openapi.Parse(data); err == nil {
				return data, base + p, nil
			}
		}
	}
	return nil, "", fmt.Errorf("no OpenAPI JSON document at %s (tried %s); pass --openapi with its URL or a file", base, strings.Join(openapi.CommonPaths, ", "))
}

// scenarioFor turns an endpoint into a scenario. It reads the list endpoint
// once so the chosen counter is one that actually exists in a real reply.
func scenarioFor(base string, e openapi.Endpoint, headers, live map[string]string, noKey bool) (config.Config, string, error) {
	delta := int64(1)
	c := config.Config{
		Schema:   config.SchemaURL,
		Name:     scenarioName(e.Method, base+e.Path),
		Scenario: "lost-response",
		Request:  config.Request{Method: e.Method, URL: base + e.Path, Headers: map[string]string{}, Body: e.Body},
		Observe:  config.Observation{URL: base + e.ListPath, ExpectedDelta: &delta},
	}
	if len(e.Body) > 0 {
		c.Request.Headers["Content-Type"] = "application/json"
	}
	for k, v := range e.Headers {
		c.Request.Headers[k] = v
	}
	for k, v := range headers {
		c.Request.Headers[k] = v
	}
	if len(headers) > 0 {
		c.Observe.Headers = map[string]string(headers)
	}
	if !noKey && !hasHeader(c.Request.Headers, "Idempotency-Key") {
		c.Request.Headers["Idempotency-Key"] = "recoverylab-{{run_id}}"
	}

	// Check the scenario before sending anything, so a base that isn't on
	// this machine never receives a request.
	if err := config.LocalURL(c.Observe.URL); err != nil {
		return c, "", err
	}
	root, err := fetchJSON(c.Observe.URL, live)
	if err != nil {
		if e.Measure == "" {
			return c, "", fmt.Errorf("couldn't read GET %s (%v) and its schema doesn't say what to count", e.ListPath, err)
		}
		c.Observe.Pointer, c.Observe.Measure = e.Pointer, measure(e.Measure)
		return c, fmt.Sprintf("counts %s from the spec (GET failed: %v)", describeCount(e.ListPath, e.Pointer, e.Measure), err), c.Validate()
	}
	found := candidates(root, "", 0)
	sort.SliceStable(found, func(i, j int) bool { return found[i].rank > found[j].rank })
	if len(found) == 0 {
		return c, "", fmt.Errorf("GET %s has nothing to count", e.ListPath)
	}
	chosen := found[0]
	for _, f := range found {
		if e.Measure != "" && f.pointer == e.Pointer && f.measure == e.Measure {
			chosen = f // The spec and the live reply agree.
			break
		}
	}
	c.Observe.Pointer, c.Observe.Measure = chosen.pointer, measure(chosen.measure)
	return c, "counts " + describeCount(e.ListPath, chosen.pointer, chosen.measure), c.Validate()
}

func measure(m string) string {
	if m == config.MeasureLength {
		return m
	}
	return "" // value is the default and reads better left out.
}

func describeCount(list, pointer, m string) string {
	if m == config.MeasureLength {
		if pointer == "" {
			return "items in GET " + list
		}
		return "items in GET " + list + " " + pointer
	}
	return "GET " + list + " " + pointer
}
