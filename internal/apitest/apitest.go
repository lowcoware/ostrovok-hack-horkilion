// Package apitest checks Go values and HTTP responses against api/openapi.yaml.
// It is used by tests only.
package apitest

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/legacy"

	"horkilion/api"
)

var (
	once   sync.Once
	doc    *openapi3.T
	router routers.Router
	err    error
)

// Spec returns the parsed and validated contract.
func Spec(t testing.TB) (*openapi3.T, routers.Router) {
	t.Helper()
	once.Do(func() {
		loader := openapi3.NewLoader()
		doc, err = loader.LoadFromData(api.OpenAPI)
		if err != nil {
			return
		}
		if err = doc.Validate(context.Background()); err != nil {
			return
		}
		// Paths in the spec are relative to the /api/v1 server.
		doc.Servers = openapi3.Servers{{URL: "/api/v1"}}
		router, err = legacy.NewRouter(doc)
	})
	if err != nil {
		t.Fatalf("load api/openapi.yaml: %v", err)
	}
	return doc, router
}

// ValidateSchema checks that v, marshalled to JSON, matches the named schema
// from components/schemas.
func ValidateSchema(t testing.TB, schema string, v any) {
	t.Helper()
	d, _ := Spec(t)
	ref, ok := d.Components.Schemas[schema]
	if !ok {
		t.Fatalf("schema %s is not in api/openapi.yaml", schema)
	}
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %s: %v", schema, err)
	}
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatalf("unmarshal %s: %v", schema, err)
	}
	if err := ref.Value.VisitJSON(generic, openapi3.EnableJSONSchema2020(), openapi3.MultiErrors()); err != nil {
		t.Errorf("%s does not match the schema: %v\n%s", schema, err, raw)
	}
}

// ValidateResponse checks a recorded response against the operation of req.
// It fails when the operation does not declare the response status.
func ValidateResponse(t testing.TB, req *http.Request, rec *httptest.ResponseRecorder) {
	t.Helper()
	_, r := Spec(t)
	route, params, err := r.FindRoute(req)
	if err != nil {
		t.Fatalf("%s %s is not in api/openapi.yaml: %v", req.Method, req.URL.Path, err)
	}
	input := &openapi3filter.ResponseValidationInput{
		RequestValidationInput: &openapi3filter.RequestValidationInput{
			Request: req, PathParams: params, Route: route,
			Options: &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc},
		},
		Status: rec.Code,
		Header: rec.Header(),
		Body:   io.NopCloser(bytes.NewReader(rec.Body.Bytes())),
		Options: &openapi3filter.Options{
			IncludeResponseStatus: true,
			MultiError:            true,
		},
	}
	if err := openapi3filter.ValidateResponse(context.Background(), input); err != nil {
		t.Errorf("%s %s -> %d does not match api/openapi.yaml: %v\n%s", req.Method, req.URL.Path, rec.Code, err, rec.Body.String())
	}
}
