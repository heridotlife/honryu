package httpapi_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/heridotlife/honryu/internal/adapters/httpapi"
)

// TestOpenAPIMatchesRoutes fails when the router and the published OpenAPI
// document drift apart. The route table is the source of truth; the document
// must describe exactly the routes that exist, no more and no fewer.
func TestOpenAPIMatchesRoutes(t *testing.T) {
	t.Parallel()

	doc := loadOpenAPI(t)

	documented := make(map[string]bool)
	for path, item := range pathsOf(t, doc) {
		for key := range item {
			// A path item legally holds non-operation keys ("parameters",
			// "summary", "description", "servers", "$ref"). Only the HTTP
			// methods describe routes.
			if !isHTTPMethod(key) {
				continue
			}
			documented[strings.ToUpper(key)+" "+path] = true
		}
	}

	registered := make(map[string]bool)
	for _, r := range httpapi.Routes() {
		// The OpenAPI path syntax is {param}, same as net/http's, so patterns
		// compare directly.
		registered[r.Method+" "+r.Pattern] = true
	}

	for route := range registered {
		if !documented[route] {
			t.Errorf("route %q is registered but missing from api/openapi.yaml", route)
		}
	}
	for route := range documented {
		if !registered[route] {
			t.Errorf("route %q is documented but not registered in the router", route)
		}
	}

	if t.Failed() {
		t.Logf("registered routes:\n  %s", strings.Join(sortedKeys(registered), "\n  "))
		t.Logf("documented routes:\n  %s", strings.Join(sortedKeys(documented), "\n  "))
	}
}

// TestOpenAPITagsMatchRouteGroups keeps the document's tags trustworthy: each
// operation's tag must be the group its route declares, so the two cannot drift.
func TestOpenAPITagsMatchRouteGroups(t *testing.T) {
	t.Parallel()

	doc := loadOpenAPI(t)

	for _, r := range httpapi.Routes() {
		item, ok := pathsOf(t, doc)[r.Pattern]
		if !ok {
			continue // reported by TestOpenAPIMatchesRoutes
		}
		op, ok := item[strings.ToLower(r.Method)].(map[string]any)
		if !ok {
			continue
		}
		tags, _ := op["tags"].([]any)
		if len(tags) == 0 {
			t.Errorf("%s %s: operation has no tags, want %q", r.Method, r.Pattern, r.Group)
			continue
		}
		if got, _ := tags[0].(string); got != r.Group {
			t.Errorf("%s %s: documented tag %q, route group %q", r.Method, r.Pattern, got, r.Group)
		}
	}
}

// TestRouteTableWellFormed checks the route table itself: entries are complete,
// patterns are unique, and every pattern is one net/http's ServeMux accepts --
// the last verified by building a router, which panics on a malformed or
// duplicate pattern.
func TestRouteTableWellFormed(t *testing.T) {
	t.Parallel()

	seen := make(map[string]bool)
	for _, r := range httpapi.Routes() {
		if r.Method == "" || r.Pattern == "" {
			t.Errorf("route with empty method or pattern: %+v", r)
		}
		if !strings.HasPrefix(r.Pattern, "/") {
			t.Errorf("route pattern %q does not start with /", r.Pattern)
		}
		if r.Group == "" {
			t.Errorf("route %s %s has no group", r.Method, r.Pattern)
		}
		key := r.Method + " " + r.Pattern
		if seen[key] {
			t.Errorf("duplicate route %q", key)
		}
		seen[key] = true
	}

	// Registers all patterns; ServeMux panics on an invalid or duplicate one.
	if h := httpapi.NewRouter(httpapi.Deps{}); h == nil {
		t.Fatal("NewRouter returned nil")
	}
}

// TestOpenAPISpecStructure validates the document's own structural
// integrity, independent of the router: it must declare OpenAPI 3.1, carry
// an info block, declare a non-empty paths object, and every path item must
// hold at least one operation -- a path with only parameters/description
// keys describes no route and is always a document bug.
func TestOpenAPISpecStructure(t *testing.T) {
	t.Parallel()

	doc := loadOpenAPI(t)

	version, ok := doc["openapi"].(string)
	if !ok || !strings.HasPrefix(version, "3.1.") {
		t.Errorf("openapi field %v, want a 3.1.x version string", doc["openapi"])
	}

	info, ok := doc["info"].(map[string]any)
	if !ok {
		t.Error("info block missing")
	} else {
		for _, field := range []string{"title", "version"} {
			if s, _ := info[field].(string); s == "" {
				t.Errorf("info.%s missing or empty", field)
			}
		}
	}

	paths := pathsOf(t, doc)
	for path, item := range paths {
		ops := 0
		for key := range item {
			if isHTTPMethod(key) {
				ops++
			}
		}
		if ops == 0 {
			t.Errorf("path %q declares no operation", path)
		}
	}
}

// TestOpenAPIRefsResolve walks the entire document -- paths and components
// alike -- and fails on any $ref that does not resolve within the document
// itself: the published spec is self-contained by contract, and a dangling
// reference is a broken contract.
func TestOpenAPIRefsResolve(t *testing.T) {
	t.Parallel()

	doc := loadOpenAPI(t)

	for _, ref := range collectRefs(doc, nil) {
		if err := resolveLocalRef(doc, ref); err != nil {
			t.Errorf("$ref %q: %v", ref, err)
		}
	}
}

// httpMethods are the operation keys OpenAPI defines for a path item.
var httpMethods = map[string]bool{
	"get": true, "put": true, "post": true, "delete": true,
	"options": true, "head": true, "patch": true, "trace": true,
}

func isHTTPMethod(key string) bool { return httpMethods[strings.ToLower(key)] }

// loadOpenAPI parses api/openapi.yaml into its generic document tree, so
// both the route comparison and the structural/ref walks see the same shape.
func loadOpenAPI(t *testing.T) map[string]any {
	t.Helper()
	// test file lives in internal/adapters/httpapi
	path := filepath.Join("..", "..", "..", "api", "openapi.yaml")
	data, err := os.ReadFile(path) //nolint:gosec // fixed repo-relative path
	if err != nil {
		t.Fatalf("read openapi.yaml: %v", err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse openapi.yaml: %v", err)
	}
	if len(doc) == 0 {
		t.Fatal("openapi.yaml parsed to an empty document")
	}
	return doc
}

// pathsOf returns the document's paths object, failing when absent -- every
// caller needs it, and an empty paths object is always a document bug.
func pathsOf(t *testing.T, doc map[string]any) map[string]map[string]any {
	t.Helper()
	raw, ok := doc["paths"].(map[string]any)
	if !ok {
		t.Fatal("openapi.yaml declares no paths object")
	}
	paths := make(map[string]map[string]any, len(raw))
	for p, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			t.Errorf("path %q is not a mapping", p)
			continue
		}
		paths[p] = m
	}
	if len(paths) == 0 {
		t.Fatal("openapi.yaml declares no paths")
	}
	return paths
}

// collectRefs appends every $ref string found anywhere in the node tree.
func collectRefs(node any, into []string) []string {
	switch v := node.(type) {
	case map[string]any:
		for k, val := range v {
			if k == "$ref" {
				if s, ok := val.(string); ok {
					into = append(into, s)
				}
			}
			into = collectRefs(val, into)
		}
	case []any:
		for _, val := range v {
			into = collectRefs(val, into)
		}
	}
	return into
}

// resolveLocalRef walks a JSON-pointer reference ("#/components/...") from
// the document root, requiring every segment to exist. Non-local references
// are rejected outright: this document is self-contained.
func resolveLocalRef(doc map[string]any, ref string) error {
	if !strings.HasPrefix(ref, "#/") {
		return errors.New("not a local reference")
	}
	var cur any = doc
	for _, seg := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
		m, ok := cur.(map[string]any)
		if !ok {
			return fmt.Errorf("segment %q: parent is not a mapping", seg)
		}
		cur, ok = m[seg]
		if !ok {
			return fmt.Errorf("segment %q not found", seg)
		}
	}
	return nil
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Phase 90's wire documentation pins: the config PUT documents the
// optional tests[].mode field and its 409 refusal, and LoadProfileEntry
// (the entry shape GET serves) carries the mode provenance -- so the
// spec cannot silently drop the simple-mode contract.
func TestOpenAPIExecutionConfigMode(t *testing.T) {
	t.Parallel()
	doc := loadOpenAPI(t)
	paths := pathsOf(t, doc)

	item := paths["/api/executions/{execution_id}/config"]
	put, ok := item["put"].(map[string]any)
	if !ok {
		t.Fatal("config PUT missing from the spec")
	}

	// tests[].mode on the JSON body.
	reqBody, _ := put["requestBody"].(map[string]any)
	jsonContent, _ := reqBody["content"].(map[string]any)
	appJSON, _ := jsonContent["application/json"].(map[string]any)
	schema, _ := appJSON["schema"].(map[string]any)
	props, _ := schema["properties"].(map[string]any)
	tests, _ := props["tests"].(map[string]any)
	items, _ := tests["items"].(map[string]any)
	itemProps, _ := items["properties"].(map[string]any)
	mode, _ := itemProps["mode"].(map[string]any)
	if mode == nil {
		t.Fatal("PUT tests[].mode not documented")
	}
	if enum, _ := mode["enum"].([]any); len(enum) != 3 {
		t.Errorf("PUT tests[].mode enum = %v, want [burst ramp soak]", mode["enum"])
	}

	// The 409 refusal is documented on the PUT.
	responses, _ := put["responses"].(map[string]any)
	if responses["409"] == nil {
		t.Error("PUT config does not document the 409 mode refusal")
	}

	// LoadProfileEntry carries mode (GET's echo contract).
	components, _ := doc["components"].(map[string]any)
	schemas, _ := components["schemas"].(map[string]any)
	entry, _ := schemas["LoadProfileEntry"].(map[string]any)
	entryProps, _ := entry["properties"].(map[string]any)
	if entryProps["mode"] == nil {
		t.Error("LoadProfileEntry does not document mode provenance")
	}
}
