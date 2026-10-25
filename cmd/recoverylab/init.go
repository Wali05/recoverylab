package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/Wali05/recoverylab/internal/config"
	"github.com/Wali05/recoverylab/internal/strictjson"
)

type headerFlags map[string]string

func (h headerFlags) String() string { return "" }

func (h headerFlags) Set(v string) error {
	name, value, ok := strings.Cut(v, ":")
	if !ok || strings.TrimSpace(name) == "" {
		return errors.New(`headers look like "Authorization: Bearer token"`)
	}
	h[strings.TrimSpace(name)] = strings.TrimSpace(value)
	return nil
}

// candidate is a value in the state response that could count the effect.
type candidate struct {
	pointer string
	measure string
	value   int64
	rank    int
}

func (c candidate) describe() string {
	pointer := c.pointer
	if pointer == "" {
		pointer = `"" (the whole response)`
	}
	if c.measure == config.MeasureLength {
		return fmt.Sprintf("%-28s list with %d items", pointer, c.value)
	}
	return fmt.Sprintf("%-28s number, currently %d", pointer, c.value)
}

func initScenario(args []string) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	method := fs.String("method", "POST", "write method: POST, PUT, PATCH, or DELETE")
	target := fs.String("url", "", "the local URL that performs the write")
	body := fs.String("body", "", "JSON body for the write")
	state := fs.String("state", "", "a local GET URL whose response shows the effect")
	pointer := fs.String("pointer", "", "JSON pointer to the counter or list; found automatically if omitted")
	out := fs.String("out", "recoverylab.json", "where to write the scenario")
	force := fs.Bool("force", false, "overwrite an existing file")
	noKey := fs.Bool("no-key", false, "don't add an Idempotency-Key header")
	headers := headerFlags{}
	fs.Var(headers, "header", `request header, such as "Authorization: Bearer token" (repeatable)`)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 || *target == "" || *state == "" {
		fmt.Fprintln(os.Stderr, "usage: recoverylab init --url WRITE_URL --state STATE_URL [--method POST] [--body JSON] [--header 'K: V'] [--pointer /count] [--out FILE]")
		return 2
	}
	if !*force {
		if _, err := os.Stat(*out); err == nil {
			fmt.Fprintf(os.Stderr, "%s already exists; pass --force to replace it\n", *out)
			return 2
		}
	}

	delta := int64(1)
	if strings.EqualFold(*method, "DELETE") {
		delta = -1
	}
	c := config.Config{
		Name:     scenarioName(*method, *target),
		Scenario: "lost-response",
		Request:  config.Request{Method: strings.ToUpper(*method), URL: *target, Headers: map[string]string{}},
		Observe:  config.Observation{URL: *state, Pointer: *pointer, ExpectedDelta: &delta},
	}
	if *body != "" {
		c.Request.Body = json.RawMessage(*body)
		c.Request.Headers["Content-Type"] = "application/json"
	}
	for k, v := range headers {
		c.Request.Headers[k] = v
	}
	if !*noKey && !hasHeader(c.Request.Headers, "Idempotency-Key") {
		c.Request.Headers["Idempotency-Key"] = "recoverylab-{{run_id}}"
	}
	if err := c.Validate(); err != nil {
		fmt.Fprintln(os.Stderr, "can't build a scenario:", err)
		return 2
	}

	s := newStyle(os.Stdout)
	root, err := fetchJSON(*state)
	if err != nil {
		fmt.Fprintf(os.Stderr, "couldn't read %s: %v\n", *state, err)
		fmt.Fprintln(os.Stderr, "Start your service first; init looks at the state endpoint to find a counter.")
		return 2
	}
	found := candidates(root, "", 0)
	sort.SliceStable(found, func(i, j int) bool { return found[i].rank > found[j].rank })
	fmt.Fprintf(os.Stdout, "\n %s %s\n\n", s.bold("Looked at"), *state)
	if len(found) == 0 {
		fmt.Fprintln(os.Stderr, "  It has no numbers or lists that could count the effect of a write.")
		fmt.Fprintln(os.Stderr, `  Expose one, for example {"order_count": 3}, or point --state at a list endpoint.`)
		return 2
	}
	chosen := found[0]
	if *pointer != "" {
		chosen = candidate{pointer: "?"}
		for _, f := range found {
			if f.pointer == *pointer {
				chosen = f
			}
		}
		if chosen.pointer == "?" {
			fmt.Fprintf(os.Stderr, "  %s isn't a number or list in that response. These are:\n", *pointer)
			for _, f := range found {
				fmt.Fprintln(os.Stderr, "    "+f.describe())
			}
			return 2
		}
	}
	for i, f := range found {
		if i == 8 {
			fmt.Fprintf(os.Stdout, "  %s\n", s.dim(fmt.Sprintf("…and %d more", len(found)-i)))
			break
		}
		line := "  " + f.describe()
		if f.pointer == chosen.pointer {
			line = s.green("→ "+f.describe()) + s.dim("  using this")
		}
		fmt.Fprintln(os.Stdout, line)
	}
	c.Observe.Pointer = chosen.pointer
	if chosen.measure == config.MeasureLength {
		c.Observe.Measure = config.MeasureLength
	}

	encoded, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if err := os.WriteFile(*out, append(encoded, '\n'), 0644); err != nil {
		fmt.Fprintln(os.Stderr, "write scenario:", err)
		return 2
	}
	fmt.Fprintf(os.Stdout, "\n %s %s\n", s.bold("Wrote"), *out)
	if len(found) > 1 && *pointer == "" {
		fmt.Fprintln(os.Stdout, " "+s.dim("If that isn't the effect you care about, rerun with --pointer, or edit observe.pointer."))
	}
	if delta < 0 {
		fmt.Fprintln(os.Stdout, " "+s.dim("DELETE should remove one item, so expected_delta is -1."))
	}
	fmt.Fprintf(os.Stdout, "\n Next:\n   recoverylab audit %s\n\n", *out)
	return 0
}

// scenarioName turns "POST http://127.0.0.1:8080/api/orders" into
// "post-api-orders".
func scenarioName(method, raw string) string {
	name := strings.ToLower(method)
	if u, err := url.Parse(raw); err == nil {
		for _, part := range strings.FieldsFunc(strings.ToLower(u.Path), func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
		}) {
			name += "-" + part
		}
	}
	return name
}

func hasHeader(h map[string]string, name string) bool {
	for k := range h {
		if strings.EqualFold(k, name) {
			return true
		}
	}
	return false
}

func fetchJSON(url string) (any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 1<<20 {
		return nil, errors.New("response is larger than 1 MiB")
	}
	if err := strictjson.RejectDuplicateKeys(data); err != nil {
		return nil, fmt.Errorf("not usable JSON: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var root any
	if err := dec.Decode(&root); err != nil {
		return nil, fmt.Errorf("not JSON: %w", err)
	}
	return root, nil
}

// candidates lists integers and arrays that could count a write. Names that
// sound like a count rank highest; a total beats a list because lists are
// often a single page. Things inside a list are skipped: the list's length
// is what changes when something is added.
func candidates(node any, pointer string, depth int) []candidate {
	if depth > 4 {
		return nil
	}
	name := strings.ToLower(pointer[strings.LastIndex(pointer, "/")+1:])
	switch v := node.(type) {
	case []any:
		rank := 2
		if depth == 0 {
			rank = 3
		}
		return []candidate{{pointer: pointer, measure: config.MeasureLength, value: int64(len(v)), rank: rank}}
	case json.Number:
		n, err := v.Int64()
		if err != nil {
			return nil
		}
		rank := 1
		for _, hint := range []string{"count", "total", "size", "num", "length"} {
			if strings.Contains(name, hint) {
				rank = 4
			}
		}
		for _, skip := range []string{"page", "limit", "offset", "per_page", "version", "port", "uptime", "status", "code", "timestamp"} {
			if name == skip {
				rank = 0
			}
		}
		return []candidate{{pointer: pointer, measure: config.MeasureValue, value: n, rank: rank}}
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var out []candidate
		for _, k := range keys {
			escaped := strings.ReplaceAll(strings.ReplaceAll(k, "~", "~0"), "/", "~1")
			out = append(out, candidates(v[k], pointer+"/"+escaped, depth+1)...)
		}
		return out
	}
	return nil
}
