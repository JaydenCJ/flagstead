// In-process tests for the HTTP API using net/http/httptest — no real
// sockets, no network. Hot reload is exercised by rewriting the flag file
// and bumping its mtime explicitly with os.Chtimes, so the tests never
// depend on filesystem timestamp granularity or wall-clock sleeps.
package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testFile = `version = 1

[flags.dark-mode]
description = "Dark theme"
enabled = false

[flags.new-checkout]
description = "New checkout flow"
enabled = true
rollout = 25

[[flags.new-checkout.rules]]
attribute = "country"
op = "in"
values = ["JP", "DE"]
rollout = 100

[config.api]
timeout_ms = 500
retries = 3
`

// harness writes the standard test file and returns the server plus the
// file path (for reload tests).
func harness(t *testing.T) (*Server, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "flags.toml")
	writeFlags(t, path, testFile, time.Unix(1_700_000_000, 0))
	store, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return New(store), path
}

// writeFlags writes content and pins the mtime so successive rewrites
// always look changed to the store, regardless of timestamp resolution.
func writeFlags(t *testing.T, path, content string, mtime time.Time) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func do(t *testing.T, s *Server, method, target string, hdr map[string]string, body string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, target, nil)
	} else {
		req = httptest.NewRequest(method, target, strings.NewReader(body))
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("response is not JSON: %v\n%s", err, rec.Body.String())
	}
	return m
}

func TestHealthzReportsFlagCountAndHash(t *testing.T) {
	s, _ := harness(t)
	rec := do(t, s, "GET", "/healthz", nil, "")
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	m := decode(t, rec)
	if m["status"] != "ok" || m["flags"] != float64(2) || m["stale"] != false {
		t.Fatalf("unexpected health: %v", m)
	}
}

func TestFlagsEndpointReturnsAllFlagsWithETag(t *testing.T) {
	s, _ := harness(t)
	rec := do(t, s, "GET", "/v1/flags", nil, "")
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	if rec.Header().Get("ETag") == "" {
		t.Fatal("missing ETag header")
	}
	if rec.Header().Get("Cache-Control") != "no-cache" {
		t.Fatal("missing Cache-Control: no-cache")
	}
	m := decode(t, rec)
	flags := m["flags"].(map[string]any)
	if len(flags) != 2 {
		t.Fatalf("want 2 flags, got %v", flags)
	}
}

func TestIfNoneMatchPollingContract(t *testing.T) {
	s, _ := harness(t)
	tag := do(t, s, "GET", "/v1/flags", nil, "").Header().Get("ETag")

	// Hit: 304, empty body, ETag repeated.
	hit := do(t, s, "GET", "/v1/flags", map[string]string{"If-None-Match": tag}, "")
	if hit.Code != 304 {
		t.Fatalf("status %d, want 304", hit.Code)
	}
	if hit.Body.Len() != 0 {
		t.Fatalf("304 must have no body, got %q", hit.Body.String())
	}
	if hit.Header().Get("ETag") != tag {
		t.Fatal("304 must repeat the ETag")
	}

	// Miss: full 200 body.
	miss := do(t, s, "GET", "/v1/flags", map[string]string{"If-None-Match": `"stale-etag"`}, "")
	if miss.Code != 200 {
		t.Fatalf("status %d, want 200 on ETag miss", miss.Code)
	}

	// Lists, wildcard and W/ weak prefix all count as hits.
	for _, inm := range []string{
		`"other", ` + tag, // list containing the tag
		"*",               // wildcard
		"W/" + tag,        // weak-comparison prefix
	} {
		rec := do(t, s, "GET", "/v1/flags", map[string]string{"If-None-Match": inm}, "")
		if rec.Code != 304 {
			t.Fatalf("If-None-Match %q: status %d, want 304", inm, rec.Code)
		}
	}
}

func TestSingleFlagEndpoint(t *testing.T) {
	s, _ := harness(t)
	rec := do(t, s, "GET", "/v1/flags/new-checkout", nil, "")
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	m := decode(t, rec)
	if m["name"] != "new-checkout" || m["rollout"] != float64(25) {
		t.Fatalf("unexpected flag: %v", m)
	}
}

func TestUnknownFlagReturns404JSONError(t *testing.T) {
	s, _ := harness(t)
	rec := do(t, s, "GET", "/v1/flags/ghost", nil, "")
	if rec.Code != 404 {
		t.Fatalf("status %d, want 404", rec.Code)
	}
	m := decode(t, rec)
	if !strings.Contains(m["error"].(string), "ghost") {
		t.Fatalf("error should name the flag: %v", m)
	}
}

func TestEvalEndpointUsesKeyAndAttributes(t *testing.T) {
	s, _ := harness(t)
	// The JP rule overrides rollout to 100, so any key is enabled.
	rec := do(t, s, "GET", "/v1/eval/new-checkout?key=user-1&attr.country=JP", nil, "")
	m := decode(t, rec)
	if m["enabled"] != true || m["reason"] != "rule" || m["rule_index"] != float64(0) {
		t.Fatalf("JP context should hit rule 0: %v", m)
	}
}

func TestEvalETagVariesByContextButNotByRepetition(t *testing.T) {
	s, _ := harness(t)
	a1 := do(t, s, "GET", "/v1/eval/new-checkout?key=a", nil, "").Header().Get("ETag")
	a2 := do(t, s, "GET", "/v1/eval/new-checkout?key=a", nil, "").Header().Get("ETag")
	b := do(t, s, "GET", "/v1/eval/new-checkout?key=b", nil, "").Header().Get("ETag")
	if a1 != a2 {
		t.Fatal("same context must produce the same eval ETag")
	}
	if a1 == b {
		t.Fatal("different keys must produce different eval ETags")
	}
}

func TestEvalBatchEvaluatesEveryFlagSorted(t *testing.T) {
	s, _ := harness(t)
	rec := do(t, s, "POST", "/v1/eval", nil, `{"key":"user-1","attributes":{"country":"JP"}}`)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	m := decode(t, rec)
	results := m["results"].([]any)
	if len(results) != 2 {
		t.Fatalf("want 2 results, got %d", len(results))
	}
	first := results[0].(map[string]any)
	if first["flag"] != "dark-mode" {
		t.Fatalf("results not sorted by flag name: %v", results)
	}
}

func TestEvalBatchSubsetAndUnknownFlag(t *testing.T) {
	s, _ := harness(t)
	rec := do(t, s, "POST", "/v1/eval", nil, `{"key":"u","flags":["dark-mode"]}`)
	m := decode(t, rec)
	if len(m["results"].([]any)) != 1 {
		t.Fatalf("subset ignored: %v", m)
	}
	rec = do(t, s, "POST", "/v1/eval", nil, `{"key":"u","flags":["ghost"]}`)
	if rec.Code != 404 {
		t.Fatalf("unknown flag in batch: status %d, want 404", rec.Code)
	}
}

func TestEvalBatchRejectsMalformedAndUnknownFields(t *testing.T) {
	s, _ := harness(t)
	if rec := do(t, s, "POST", "/v1/eval", nil, `{not json`); rec.Code != 400 {
		t.Fatalf("malformed body: status %d, want 400", rec.Code)
	}
	if rec := do(t, s, "POST", "/v1/eval", nil, `{"kee":"typo"}`); rec.Code != 400 {
		t.Fatalf("unknown field must 400 (typo protection), got %d", rec.Code)
	}
}

func TestConfigEndpointAndPathLookup(t *testing.T) {
	s, _ := harness(t)
	m := decode(t, do(t, s, "GET", "/v1/config", nil, ""))
	api := m["config"].(map[string]any)["api"].(map[string]any)
	if api["retries"] != float64(3) {
		t.Fatalf("config tree wrong: %v", m)
	}

	leaf := decode(t, do(t, s, "GET", "/v1/config/api/timeout_ms", nil, ""))
	if leaf["value"] != float64(500) || leaf["path"] != "api/timeout_ms" {
		t.Fatalf("leaf lookup wrong: %v", leaf)
	}

	sub := decode(t, do(t, s, "GET", "/v1/config/api", nil, ""))
	if sub["value"].(map[string]any)["retries"] != float64(3) {
		t.Fatalf("subtree lookup wrong: %v", sub)
	}
}

func TestConfigUnknownPathReturns404(t *testing.T) {
	s, _ := harness(t)
	if rec := do(t, s, "GET", "/v1/config/api/ghost", nil, ""); rec.Code != 404 {
		t.Fatalf("status %d, want 404", rec.Code)
	}
	// A path descending THROUGH a scalar is also unknown.
	if rec := do(t, s, "GET", "/v1/config/api/retries/deeper", nil, ""); rec.Code != 404 {
		t.Fatalf("status %d, want 404 through scalar", rec.Code)
	}
}

func TestHotReloadChangesResponsesAndETag(t *testing.T) {
	s, path := harness(t)
	oldTag := do(t, s, "GET", "/v1/flags", nil, "").Header().Get("ETag")

	updated := testFile + "\n[flags.brand-new]\nenabled = true\n"
	writeFlags(t, path, updated, time.Unix(1_700_000_100, 0))

	rec := do(t, s, "GET", "/v1/flags", nil, "")
	m := decode(t, rec)
	if _, ok := m["flags"].(map[string]any)["brand-new"]; !ok {
		t.Fatalf("new flag not served after reload: %v", m)
	}
	if rec.Header().Get("ETag") == oldTag {
		t.Fatal("ETag must change when the file changes")
	}
	// The old ETag now misses, so a poller gets the fresh body.
	if rec := do(t, s, "GET", "/v1/flags", map[string]string{"If-None-Match": oldTag}, ""); rec.Code != 200 {
		t.Fatalf("stale ETag should yield 200, got %d", rec.Code)
	}
}

func TestBrokenEditKeepsServingLastGoodSnapshot(t *testing.T) {
	s, path := harness(t)
	writeFlags(t, path, "[flags.broken\nenabled = tru\n", time.Unix(1_700_000_200, 0))

	// Data endpoints keep answering from the last good snapshot.
	rec := do(t, s, "GET", "/v1/flags", nil, "")
	if rec.Code != 200 {
		t.Fatalf("status %d — a bad edit must not break clients", rec.Code)
	}
	if len(decode(t, rec)["flags"].(map[string]any)) != 2 {
		t.Fatal("last good snapshot lost")
	}

	// healthz surfaces the problem for operators.
	h := decode(t, do(t, s, "GET", "/healthz", nil, ""))
	if h["status"] != "degraded" || h["stale"] != true {
		t.Fatalf("healthz should be degraded: %v", h)
	}
	if !strings.Contains(h["error"].(string), "line") {
		t.Fatalf("healthz error should carry the parse error: %v", h)
	}

	// Fixing the file recovers automatically.
	writeFlags(t, path, testFile, time.Unix(1_700_000_300, 0))
	h = decode(t, do(t, s, "GET", "/healthz", nil, ""))
	if h["status"] != "ok" {
		t.Fatalf("healthz should recover: %v", h)
	}
}

func TestNewStoreRefusesInvalidInitialFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "flags.toml")
	writeFlags(t, path, "not toml at all [", time.Unix(1_700_000_000, 0))
	if _, err := NewStore(path); err == nil {
		t.Fatal("NewStore must fail eagerly on an invalid file")
	}
}

func TestWrongMethodAndUnknownPath(t *testing.T) {
	s, _ := harness(t)
	if rec := do(t, s, "POST", "/v1/flags", nil, "{}"); rec.Code != 405 {
		t.Fatalf("status %d, want 405", rec.Code)
	}
	if rec := do(t, s, "GET", "/v1/eval", nil, ""); rec.Code != 405 {
		t.Fatalf("status %d, want 405 for GET on batch eval", rec.Code)
	}
	if rec := do(t, s, "GET", "/v2/everything", nil, ""); rec.Code != 404 {
		t.Fatalf("status %d, want 404", rec.Code)
	}
}
