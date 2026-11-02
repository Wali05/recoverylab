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
