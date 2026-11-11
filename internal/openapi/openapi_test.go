package openapi

import (
	"encoding/json"
	"strings"
	"testing"
)

const shop = `{
  "openapi": "3.1.0",
  "info": {"title": "Shop"},
  "security": [{"bearer": []}],
  "paths": {
    "/orders": {
      "get": {"responses": {"200": {"content": {"application/json": {"schema": {"$ref": "#/components/schemas/OrderPage"}}}}}},
      "post": {
        "parameters": [{"name": "X-Tenant", "in": "header", "required": true, "schema": {"type": "string", "example": "acme"}}],
        "requestBody": {"content": {"application/json; charset=utf-8": {"schema": {"$ref": "#/components/schemas/NewOrder"}}}}
      }
    },
    "/customers": {
      "get": {"responses": {"200": {"content": {"application/json": {"schema": {"type": "object", "properties": {"data": {"type": "array"}, "page": {"type": "integer"}}}}}}}},
      "post": {"security": [], "requestBody": {"content": {"application/json": {"schema": {"type": "object", "required": ["email"], "properties": {"email": {"type": "string", "format": "email"}, "nickname": {"type": "string"}}}}}}}
    },
    "/orders/{id}/refunds": {"post": {"requestBody": {"content": {"application/json": {"schema": {"type": "object"}}}}}},
    "/orders/{id}": {"patch": {"requestBody": {"content": {"application/json": {"schema": {"type": "object"}}}}}},
    "/login": {"post": {"requestBody": {"content": {"application/x-www-form-urlencoded": {"schema": {"type": "object"}}}}}},
    "/webhooks": {"post": {}}
  },
  "components": {"schemas": {
    "Base": {"type": "object", "required": ["currency"], "properties": {"currency": {"type": "string", "enum": ["EUR", "USD"]}}},
    "NewOrder": {"allOf": [{"$ref": "#/components/schemas/Base"}, {"type": "object", "required": ["sku", "quantity", "items", "id"], "properties": {
      "id": {"type": "string", "readOnly": true},
      "sku": {"type": ["string", "null"], "minLength": 3, "maxLength": 12},
      "quantity": {"type": "integer", "minimum": 2},
      "gift": {"type": "boolean"},
      "items": {"type": "array", "minItems": 2, "items": {"type": "number", "default": 9.5}}
    }}]},
    "OrderPage": {"type": "object", "properties": {"content": {"type": "array"}, "totalElements": {"type": "integer"}}}
  }}
}`

func TestFindsWriteEndpoints(t *testing.T) {
	doc, err := Parse([]byte(shop))
	if err != nil {
		t.Fatal(err)
	}
	if doc.Title() != "Shop" {
		t.Fatalf("title %q", doc.Title())
	}
	byPath := map[string]Endpoint{}
	for _, e := range doc.Writes(map[string]string{"id": "42"}) {
		byPath[e.Method+" "+e.Path] = e
	}

	order := byPath["POST /orders"]
	if order.Skip != "" || order.ListPath != "/orders" || order.Pointer != "/totalElements" || order.Measure != "value" || !order.Secured || order.Headers["X-Tenant"] != "acme" {
		t.Fatalf("orders: %+v", order)
	}
	var body map[string]any
	if err := json.Unmarshal(order.Body, &body); err != nil {
		t.Fatal(err)
	}
	if body["currency"] != "EUR" || body["quantity"] != float64(2) || len(body["items"].([]any)) != 2 || body["items"].([]any)[0] != 9.5 {
		t.Fatalf("order body: %s", order.Body)
	}
	if _, ok := body["id"]; ok {
		t.Fatalf("read-only id was sent: %s", order.Body)
	}
	if _, ok := body["gift"]; ok {
		t.Fatalf("optional field was sent: %s", order.Body)
	}
	if sku := body["sku"].(string); len(strings.ReplaceAll(sku, "{{run_id}}", "0123456789abcdef")) > 12 || len(sku) < 3 {
		t.Fatalf("sku ignores its length limits: %q", sku)
	}

	customer := byPath["POST /customers"]
	if customer.Secured || customer.Pointer != "/data" || customer.Measure != "length" || !strings.Contains(string(customer.Body), `"recoverylab+{{run_id}}@example.com"`) || strings.Contains(string(customer.Body), "nickname") {
		t.Fatalf("customers: %+v %s", customer, customer.Body)
	}
	if refund := byPath["POST /orders/42/refunds"]; !strings.Contains(refund.Skip, "no GET /orders/{id}/refunds") {
		t.Fatalf("filled path param: %+v", refund)
	}
	if patch := byPath["PATCH /orders/42"]; !strings.Contains(patch.Skip, "changes a resource") {
		t.Fatalf("patch: %+v", patch)
	}
	if login := byPath["POST /login"]; login.Skip != "takes a body that isn't JSON" {
		t.Fatalf("form body: %+v", login)
	}
	if hook := byPath["POST /webhooks"]; len(hook.Body) != 0 {
		t.Fatalf("no request body: %+v", hook)
	}

	missing := doc.Writes(nil)
	for _, e := range missing {
		if strings.Contains(e.Path, "{id}") && !strings.Contains(e.Skip, "--param id=VALUE") {
			t.Fatalf("missing path param: %+v", e)
		}
	}
}

func TestSwagger2(t *testing.T) {
	doc, err := Parse([]byte(`{"swagger": "2.0", "paths": {"/pets": {
		"get": {"responses": {"200": {"schema": {"type": "array", "items": {"$ref": "#/definitions/Pet"}}}}},
		"post": {"parameters": [
			{"in": "body", "name": "pet", "schema": {"$ref": "#/definitions/Pet"}},
			{"in": "header", "name": "idempotency-key", "required": true, "type": "string"}
		]}}},
		"definitions": {"Pet": {"type": "object", "required": ["name"], "properties": {"name": {"type": "string", "example": "Rex"}}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	e := doc.Writes(nil)[0]
	if e.Skip != "" || string(e.Body) != `{"name":"Rex"}` || e.Pointer != "" || e.Measure != "length" || len(e.Headers) != 0 {
		t.Fatalf("swagger 2: %+v %s", e, e.Body)
	}
}

func TestRejectsOtherDocuments(t *testing.T) {
	for _, doc := range []string{`{"openapi": "2.5"}`, `{"name": "x"}`, `openapi: 3.0.0`} {
		if _, err := Parse([]byte(doc)); err == nil {
			t.Errorf("accepted %s", doc)
		}
	}
}

func example(t *testing.T, schema string) string {
	t.Helper()
	doc, err := Parse([]byte(`{"openapi":"3.1.0","paths":{"/things":{"get":{},"post":{"requestBody":{"content":{"application/json":{"schema":` + schema + `}}}}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	return string(doc.Writes(nil)[0].Body)
}

func TestNumbersStayInsideTheirBounds(t *testing.T) {
	for schema, want := range map[string]string{
		`{"type":"integer"}`:                                           `1`,
		`{"type":"integer","minimum":5}`:                               `5`,
		`{"type":"integer","minimum":5,"exclusiveMinimum":true}`:       `6`,  // OpenAPI 3.0
		`{"type":"integer","exclusiveMinimum":10}`:                     `11`, // OpenAPI 3.1
		`{"type":"integer","exclusiveMinimum":0}`:                      `1`,
		`{"type":"number","exclusiveMaximum":0}`:                       `-1`,
		`{"type":"integer","maximum":-3}`:                              `-3`,
		`{"type":"integer","maximum":0,"exclusiveMaximum":true}`:       `-1`,
		`{"type":"integer","minimum":3,"multipleOf":5}`:                `5`,
		`{"type":"integer","minimum":1,"maximum":9,"multipleOf":10}`:   `0`,
		`{"type":"number","minimum":0.5}`:                              `0.5`,
		`{"type":"integer","minimum":0.5}`:                             `1`,
		`{"type":"integer","minimum":1e300}`:                           `1`,
		`{"type":"integer","minimum":2,"exclusiveMinimum":7}`:          `8`,
		`{"type":"integer","maximum":20,"exclusiveMaximum":4}`:         `1`,
		`{"type":"integer","minimum":10,"maximum":20,"multipleOf":7}`:  `14`,
		`{"type":"integer","exclusiveMinimum":2,"exclusiveMaximum":4}`: `3`,
	} {
		if got := example(t, schema); got != want {
			t.Errorf("%s: got %s, want %s", schema, got, want)
		}
	}
}

// A schema's own limits must not decide how much memory an example takes.
func TestHugeLimitsDoNotBuildHugeBodies(t *testing.T) {
	body := example(t, `{"type":"object","properties":{
		"tags":{"type":"array","minItems":400000000,"items":{"type":"string"}},
		"note":{"type":"string","minLength":900000000}}}`)
	if len(body) > 16*1024 {
		t.Fatalf("body is %d bytes", len(body))
	}
	var decoded struct {
		Tags []string
		Note string
	}
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		t.Fatal(err)
	}
	note := strings.ReplaceAll(decoded.Note, "{{run_id}}", "0123456789abcdef")
	if len(decoded.Tags) != maxExampleItems || len(note) != maxExampleLength {
		t.Fatalf("got %d tags and a %d-character note", len(decoded.Tags), len(note))
	}
}

// "required" can name a property that another allOf branch declares. Building
// a null for it used to overwrite the real value.
func TestRequiredWithoutASchemaIsLeftOut(t *testing.T) {
	if got := example(t, `{"type":"object","required":["sku","ghost"],"properties":{"sku":{"type":"string","example":"book"}}}`); got != `{"sku":"book"}` {
		t.Errorf("undeclared required property: %s", got)
	}
	got := example(t, `{"allOf":[
		{"type":"object","properties":{"sku":{"type":"string","example":"book"}}},
		{"type":"object","required":["sku"]}]}`)
	if got != `{"sku":"book"}` {
		t.Errorf("allOf lost the declared value: %s", got)
	}
}

func TestUUIDsAreFreshPerRun(t *testing.T) {
	if got := example(t, `{"type":"string","format":"uuid"}`); got != `"{{uuid}}"` {
		t.Errorf("uuid: %s", got)
	}
	// Too short for a UUID: fall back to something that fits.
	if got := example(t, `{"type":"string","format":"uuid","maxLength":8}`); got != `"00000000"` {
		t.Errorf("short uuid: %s", got)
	}
}
