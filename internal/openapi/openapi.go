// Package openapi reads an OpenAPI 3 or Swagger 2 JSON document and finds the
// write endpoints worth testing, with an example body for each and the list
// endpoint that can count its effect.
package openapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// Endpoint is one write operation found in the document. Skip explains why
// it can't become a scenario without more input.
type Endpoint struct {
	Method  string
	Path    string
	Summary string
	Body    json.RawMessage
	Headers map[string]string
	Secured bool
	// ListPath is the GET endpoint on the same path, if the document has one.
	ListPath string
	// Pointer and Measure are a guess at what in the list response counts the
	// effect, taken from its schema. The caller can confirm it against the
	// live response.
	Pointer string
	Measure string
	Skip    string
}

type Document struct {
	root    map[string]any
	swagger bool
}

// Parse accepts OpenAPI 3.x and Swagger 2.0 in JSON. YAML isn't supported
// because RecoveryLab sticks to Go's standard library; every common
// framework also serves its document as JSON.
func Parse(data []byte) (*Document, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var root map[string]any
	if err := dec.Decode(&root); err != nil {
		return nil, fmt.Errorf("not a JSON OpenAPI document: %w", err)
	}
	d := &Document{root: root}
	if v, ok := root["openapi"].(string); ok && strings.HasPrefix(v, "3.") {
		return d, nil
	}
	if v, ok := root["swagger"].(string); ok && strings.HasPrefix(v, "2.") {
		d.swagger = true
		return d, nil
	}
	return nil, errors.New(`not an OpenAPI 3 or Swagger 2 document (no "openapi": "3.x" or "swagger": "2.0" field)`)
}

func (d *Document) Title() string {
	info, _ := d.root["info"].(map[string]any)
	title, _ := info["title"].(string)
	return title
}

// Writes lists POST, PUT, and PATCH operations in path order. DELETE is left
// out: it needs an existing resource, which a generated scenario can't name.
// params fills path parameters such as {id}.
func (d *Document) Writes(params map[string]string) []Endpoint {
	paths, _ := d.root["paths"].(map[string]any)
	names := make([]string, 0, len(paths))
	for p := range paths {
		names = append(names, p)
	}
	sort.Strings(names)
	var out []Endpoint
	for _, path := range names {
		item, _ := paths[path].(map[string]any)
		for _, method := range []string{"post", "put", "patch"} {
			op, ok := item[method].(map[string]any)
			if !ok {
				continue
			}
			out = append(out, d.endpoint(path, method, item, op, params))
		}
	}
	return out
}

func (d *Document) endpoint(path, method string, item, op map[string]any, params map[string]string) Endpoint {
	e := Endpoint{Method: strings.ToUpper(method), Path: path, Headers: map[string]string{}}
	e.Summary, _ = op["summary"].(string)
	e.Secured = d.secured(op)

	filled, missing := fillPath(path, params)
	if len(missing) > 0 {
		e.Skip = fmt.Sprintf("needs a value for {%s}; pass --param %s=VALUE", missing[0], missing[0])
		return e
	}
	e.Path = filled

	for _, p := range d.parameters(item, op) {
		in, _ := p["in"].(string)
		name, _ := p["name"].(string)
		required, _ := p["required"].(bool)
		switch {
		case in == "header" && strings.EqualFold(name, "Idempotency-Key"):
			// The caller adds a fresh key per run.
		case in == "header" && required && !strings.EqualFold(name, "Authorization"):
			value := d.example(d.paramSchema(p), 0)
			if value == nil {
				value = "recoverylab"
			}
			e.Headers[name] = fmt.Sprint(value)
		case in == "body" && d.swagger:
			e.Body = d.encode(d.example(p["schema"], 0))
		}
	}
	if !d.swagger {
		if rb, ok := d.resolve(op["requestBody"]).(map[string]any); ok {
			if schema, ok := jsonSchema(rb["content"]); ok {
				e.Body = d.encode(d.example(schema, 0))
			} else if rb["content"] != nil {
				e.Skip = "takes a body that isn't JSON"
				return e
			}
		}
	}

	paths, _ := d.root["paths"].(map[string]any)
	if list, ok := paths[path].(map[string]any); ok {
		if get, ok := list["get"].(map[string]any); ok && e.Method == "POST" {
			e.ListPath = filled
			e.Pointer, e.Measure = d.countGuess(get)
		}
	}
	if e.ListPath == "" {
		// PUT and PATCH usually change one resource rather than adding one,
		// so a list count wouldn't move. Those need a hand-written check.
		if e.Method == "POST" {
			e.Skip = "no GET " + path + " to count the effect; write the observe block by hand"
		} else {
			e.Skip = e.Method + " changes a resource instead of adding one; write the observe block by hand"
		}
	}
	return e
}

func fillPath(path string, params map[string]string) (string, []string) {
	var missing []string
	var b strings.Builder
	for {
		start := strings.Index(path, "{")
		end := strings.Index(path, "}")
		if start < 0 || end < start {
			b.WriteString(path)
			return b.String(), missing
		}
		name := path[start+1 : end]
		b.WriteString(path[:start])
		if v, ok := params[name]; ok {
			b.WriteString(v)
		} else {
			missing = append(missing, name)
			b.WriteString(path[start : end+1])
		}
		path = path[end+1:]
	}
}

func (d *Document) secured(op map[string]any) bool {
	sec, ok := op["security"].([]any)
	if !ok {
		sec, _ = d.root["security"].([]any)
	}
	for _, req := range sec {
		if m, ok := req.(map[string]any); ok && len(m) > 0 {
			return true
		}
	}
	return false
}

func (d *Document) parameters(item, op map[string]any) []map[string]any {
	var out []map[string]any
	for _, list := range []any{item["parameters"], op["parameters"]} {
		params, _ := list.([]any)
		for _, p := range params {
			if m, ok := d.resolve(p).(map[string]any); ok {
				out = append(out, m)
			}
		}
	}
	return out
}

func (d *Document) paramSchema(p map[string]any) any {
	if s, ok := p["schema"]; ok {
		return s
	}
	return p // Swagger 2 puts type and format on the parameter itself.
}

func jsonSchema(content any) (any, bool) {
	m, _ := content.(map[string]any)
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		media := strings.ToLower(strings.TrimSpace(strings.Split(k, ";")[0]))
		if media == "application/json" || strings.HasSuffix(media, "+json") {
			mt, _ := m[k].(map[string]any)
			return mt["schema"], true
		}
	}
	return nil, false
}

// resolve follows local $ref values such as #/components/schemas/Order.
func (d *Document) resolve(node any) any {
	for i := 0; i < 16; i++ {
		m, ok := node.(map[string]any)
		if !ok {
			return node
		}
		ref, ok := m["$ref"].(string)
		if !ok || !strings.HasPrefix(ref, "#/") {
			return node
		}
		var cur any = d.root
		for _, part := range strings.Split(ref[2:], "/") {
			part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
			parent, _ := cur.(map[string]any)
			cur = parent[part]
		}
		node = cur
	}
	return node
}

func (d *Document) encode(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return data
}

// example builds a small value that satisfies a schema: declared examples
// and defaults first, then the first enum value, then something plausible
// for the type and format. Only required properties are filled when the
// schema lists them, which keeps bodies close to what a real client sends.
func (d *Document) example(node any, depth int) any {
	s, ok := d.resolve(node).(map[string]any)
	if !ok || depth > 8 {
		return nil
	}
	for _, key := range []string{"example", "default"} {
		if v, ok := s[key]; ok {
			return v
		}
	}
	if list, ok := s["examples"].([]any); ok && len(list) > 0 {
		return list[0]
	}
	if list, ok := s["enum"].([]any); ok && len(list) > 0 {
		return list[0]
	}
	if all, ok := s["allOf"].([]any); ok {
		merged := map[string]any{}
		for _, part := range all {
			if m, ok := d.example(part, depth+1).(map[string]any); ok {
				for k, v := range m {
					merged[k] = v
				}
			}
		}
		return merged
	}
	for _, key := range []string{"oneOf", "anyOf"} {
		if list, ok := s[key].([]any); ok && len(list) > 0 {
			return d.example(list[0], depth+1)
		}
	}
	typ := schemaType(s)
	switch typ {
	case "object":
		props, _ := s["properties"].(map[string]any)
		want := map[string]bool{}
		if req, ok := s["required"].([]any); ok && len(req) > 0 {
			for _, r := range req {
				if name, ok := r.(string); ok {
					want[name] = true
				}
			}
		} else {
			for name := range props {
				want[name] = true
			}
		}
		out := map[string]any{}
		for name := range want {
			// A name that is required here but declared elsewhere (another
			// allOf branch, say) has no schema to build a value from.
			prop, ok := d.resolve(props[name]).(map[string]any)
			if !ok {
				continue
			}
			if ro, _ := prop["readOnly"].(bool); ro {
				continue
			}
			out[name] = d.example(props[name], depth+1)
		}
		return out
	case "array":
		n := 1
		if min, ok := number(s["minItems"]); ok && min > 1 {
			n = int(math.Min(min, maxExampleItems))
		}
		items := make([]any, n)
		for i := range items {
			items[i] = d.example(s["items"], depth+1)
		}
		return items
	case "integer", "number":
		return numberExample(s, typ == "integer")
	case "boolean":
		return true
	case "string":
		return stringExample(s)
	}
	return nil
}

// A document is input like any other: limits it declares must not decide
// how much memory an example takes.
const (
	maxExampleItems  = 16
	maxExampleLength = 4096
)

// numberExample picks a value inside the schema's bounds: the minimum when
// there is one, otherwise 1, pulled down to the maximum if 1 is too big.
// Exclusive bounds come as a flag next to minimum in OpenAPI 3.0 and as a
// number of their own in 3.1.
func numberExample(s map[string]any, integer bool) any {
	step := 1.0
	lo, hasLo := number(s["minimum"])
	if flag, _ := s["exclusiveMinimum"].(bool); flag && hasLo {
		lo += step
	} else if ex, ok := number(s["exclusiveMinimum"]); ok && (!hasLo || ex >= lo) {
		lo, hasLo = ex+step, true
	}
	hi, hasHi := number(s["maximum"])
	if flag, _ := s["exclusiveMaximum"].(bool); flag && hasHi {
		hi -= step
	} else if ex, ok := number(s["exclusiveMaximum"]); ok && (!hasHi || ex <= hi) {
		hi, hasHi = ex-step, true
	}
	v := 1.0
	if hasLo {
		v = lo
	}
	if hasHi && v > hi {
		v = hi
	}
	if m, ok := number(s["multipleOf"]); ok && m > 0 {
		v = math.Ceil(v/m) * m
		if hasHi && v > hi {
			v = math.Floor(hi/m) * m
		}
	}
	if integer {
		v = math.Ceil(v)
		if hasHi && v > hi {
			v = math.Floor(hi)
		}
		if v > math.MaxInt64/2 || v < math.MinInt64/2 || math.IsNaN(v) {
			return int64(1)
		}
		return int64(v)
	}
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 1.0
	}
	return v
}

func schemaType(s map[string]any) string {
	switch t := s["type"].(type) {
	case string:
		return t
	case []any: // OpenAPI 3.1 allows ["string", "null"].
		for _, v := range t {
			if name, ok := v.(string); ok && name != "null" {
				return name
			}
		}
	}
	if _, ok := s["properties"]; ok {
		return "object"
	}
	return ""
}

func number(v any) (float64, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	f, err := n.Float64()
	return f, err == nil
}

// stringExample includes {{run_id}} or {{uuid}} where it can, so fields with a unique
// constraint (email, username, SKU) don't collide on the next run. The retry
// inside one run still sends exactly the same body.
func stringExample(s map[string]any) string {
	format, _ := s["format"].(string)
	value, plain := "recoverylab-{{run_id}}", "recoverylab"
	switch format {
	case "email":
		value, plain = "recoverylab+{{run_id}}@example.com", "recoverylab@example.com"
	case "date-time":
		value = time.Now().UTC().Format(time.RFC3339)
	case "date":
		value = time.Now().UTC().Format("2006-01-02")
	case "uuid":
		// A fixed UUID would collide with itself on the second run.
		value, plain = "{{uuid}}", "00000000-0000-4000-8000-000000000000"
	case "uri", "url":
		value, plain = "https://example.com/recoverylab/{{run_id}}", "https://example.com/recoverylab"
	case "ipv4":
		value = "127.0.0.1"
	}
	if !strings.Contains(value, "{{") {
		plain = value
	}
	// Once filled in, a run ID is 16 characters and a UUID is 36.
	length := len(strings.NewReplacer("{{run_id}}", strings.Repeat("x", 16), "{{uuid}}", strings.Repeat("x", 36)).Replace(value))
	if max, ok := number(s["maxLength"]); ok && max > 0 && float64(length) > max {
		value = plain
		if len(value) > int(max) {
			value = value[:int(max)]
		}
		length = len(value)
	}
	if min, ok := number(s["minLength"]); ok && float64(length) < min {
		value += strings.Repeat("x", int(math.Min(min, maxExampleLength))-length)
	}
	return value
}

// countGuess reads a list operation's 200 response schema and picks what
// grows when a resource is added: a total or count field if there is one
// (a list may be only the first page), otherwise the array itself.
func (d *Document) countGuess(get map[string]any) (string, string) {
	responses, _ := get["responses"].(map[string]any)
	resp, _ := d.resolve(responses["200"]).(map[string]any)
	var schema any
	if d.swagger {
		schema = resp["schema"]
	} else {
		schema, _ = jsonSchema(resp["content"])
	}
	s, ok := d.resolve(schema).(map[string]any)
	if !ok {
		return "", ""
	}
	if schemaType(s) == "array" {
		return "", "length"
	}
	props, _ := s["properties"].(map[string]any)
	names := make([]string, 0, len(props))
	for k := range props {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, hint := range []string{"total", "totalElements", "total_count", "totalCount", "count"} {
		for _, name := range names {
			if name == hint {
				if p, ok := d.resolve(props[name]).(map[string]any); ok && schemaType(p) == "integer" {
					return "/" + escape(name), "value"
				}
			}
		}
	}
	var arrays []string
	for _, name := range names {
		if p, ok := d.resolve(props[name]).(map[string]any); ok && schemaType(p) == "array" {
			arrays = append(arrays, name)
		}
	}
	for _, hint := range []string{"items", "data", "results", "content", "records", "rows"} {
		for _, name := range arrays {
			if name == hint {
				return "/" + escape(name), "length"
			}
		}
	}
	if len(arrays) > 0 {
		return "/" + escape(arrays[0]), "length"
	}
	return "", ""
}

func escape(name string) string {
	return strings.ReplaceAll(strings.ReplaceAll(name, "~", "~0"), "/", "~1")
}

// CommonPaths are where popular frameworks serve their JSON document by
// default: FastAPI, springdoc, ASP.NET Core (built-in and Swashbuckle),
// NestJS with SwaggerModule.setup("api"), Fastify, and plain swagger.json.
var CommonPaths = []string{
	"/openapi.json",
	"/v3/api-docs",
	"/openapi/v1.json",
	"/swagger/v1/swagger.json",
	"/api-json",
	"/docs-json",
	"/documentation/json",
	"/swagger.json",
	"/api/openapi.json",
	"/v2/api-docs",
}
