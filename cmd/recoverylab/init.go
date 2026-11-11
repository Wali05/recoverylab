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
	"unicode"

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
		return flagExit(err)
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
		Schema:   config.SchemaURL,
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
	if len(headers) > 0 {
		// Auth that the write needs, the state endpoint usually needs too.
		c.Observe.Headers = map[string]string(headers)
	}
	if !*noKey && !hasHeader(c.Request.Headers, "Idempotency-Key") {
		c.Request.Headers["Idempotency-Key"] = "recoverylab-{{run_id}}"
	}
	if err := c.Validate(); err != nil {
		fmt.Fprintln(os.Stderr, "can't build a scenario:", err)
		return 2
	}

	s := newStyle(os.Stdout)
	// The file keeps {{env:NAME}}; the live read needs the real value.
	live, err := config.ExpandHeaders(headers)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	root, err := fetchJSON(*state, live)
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

// fetch GETs a local URL directly, without proxies or redirects, and
// refuses bodies over limit bytes.
func fetch(url string, headers map[string]string, limit int64) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		if strings.EqualFold(k, "Host") {
			req.Host = v // Go ignores a Host entry in the header map.
			continue
		}
		req.Header.Set(k, v)
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
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("response is larger than %d MiB", limit>>20)
	}
	return data, nil
}

func fetchJSON(url string, headers map[string]string) (any, error) {
	data, err := fetch(url, headers, 1<<20)
	if err != nil {
		return nil, err
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
	name := pointer[strings.LastIndex(pointer, "/")+1:]
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
		return []candidate{{pointer: pointer, measure: config.MeasureValue, value: n, rank: counterRank(name)}}
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

// counterRank scores how much a field name sounds like "how many exist".
// It reads whole words, so order_count and totalElements score high while
// account_number and totalPages don't. A list scores 2 or 3.
//
//	5  a total: total, totalElements, total_count
//	4  a count: count, orderCount, num_orders, queue_size
//	1  some other number
//	0  paging and bookkeeping: page, totalPages, page_size, limit, version
func counterRank(name string) int {
	words := nameWords(name)
	has := func(want ...string) bool {
		for _, w := range words {
			for _, x := range want {
				if w == x {
					return true
				}
			}
		}
		return false
	}
	if has("page", "pages") {
		return 0
	}
	if len(words) == 1 && has("limit", "offset", "version", "port", "uptime", "status", "code", "timestamp", "id") {
		return 0
	}
	// A bare "size" or "number" is the page size or page number in Spring's
	// Page and similar envelopes.
	if len(words) == 1 && has("size", "number") {
		return 1
	}
	if has("total") {
		return 5
	}
	if has("count", "num", "size", "length") {
		return 4
	}
	for i, w := range words {
		if w == "number" && i+1 < len(words) && words[i+1] == "of" {
			return 4
		}
	}
	return 1
}

// nameWords splits snake_case, kebab-case, and camelCase into lower-case
// words: "totalElements" and "total_elements" both give [total elements].
func nameWords(name string) []string {
	var words []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			words = append(words, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	runes := []rune(name)
	for i, r := range runes {
		switch {
		case !unicode.IsLetter(r) && !unicode.IsDigit(r):
			flush()
			continue
		case unicode.IsUpper(r) && i > 0 && (unicode.IsLower(runes[i-1]) || (i+1 < len(runes) && unicode.IsLower(runes[i+1]) && unicode.IsUpper(runes[i-1]))):
			flush()
		}
		cur = append(cur, r)
	}
	flush()
	return words
}
