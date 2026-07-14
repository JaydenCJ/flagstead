// Package server exposes the flag file over a small, cache-friendly HTTP
// API. Every snapshot endpoint carries a strong ETag derived from the
// file's SHA-256, so clients poll with If-None-Match and pay one cheap
// 304 per interval until the file actually changes; evaluation endpoints
// get an ETag too (file hash + canonical query), so even per-user answers
// are pollable. The server binds loopback by default, sends nothing
// anywhere, and never breaks running clients on a bad file edit.
package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/JaydenCJ/flagstead/internal/eval"
)

// Server is the flagstead HTTP API. It implements http.Handler.
type Server struct {
	store *Store
	mux   *http.ServeMux
}

// New builds the API around a store.
func New(store *Store) *Server {
	s := &Server{store: store, mux: http.NewServeMux()}
	s.mux.HandleFunc("GET /healthz", s.handleHealth)
	s.mux.HandleFunc("GET /v1/flags", s.handleFlags)
	s.mux.HandleFunc("GET /v1/flags/{name}", s.handleFlag)
	s.mux.HandleFunc("GET /v1/eval/{name}", s.handleEvalOne)
	s.mux.HandleFunc("POST /v1/eval", s.handleEvalBatch)
	s.mux.HandleFunc("GET /v1/config", s.handleConfig)
	s.mux.HandleFunc("GET /v1/config/{path...}", s.handleConfigPath)
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

// Serve runs the API on an already-bound listener via http.Server.
func (s *Server) HTTPServer() *http.Server {
	return &http.Server{
		Handler:           s,
		ReadHeaderTimeout: 5 * time.Second,
	}
}

// --- handlers ---------------------------------------------------------------

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	f, err := s.store.Snapshot()
	resp := map[string]any{
		"status": "ok",
		"flags":  len(f.Flags),
		"hash":   f.Hash,
		"stale":  false,
	}
	if err != nil {
		// Still HTTP 200: the server IS healthy, it serves the last good
		// snapshot; the payload tells operators the file needs fixing.
		resp["status"] = "degraded"
		resp["stale"] = true
		resp["error"] = err.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleFlags(w http.ResponseWriter, r *http.Request) {
	f, _ := s.store.Snapshot()
	tag := etagFor("flags", f.Hash)
	if writeETag(w, r, tag) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"version": f.Version,
		"hash":    f.Hash,
		"flags":   f.Flags,
	})
}

func (s *Server) handleFlag(w http.ResponseWriter, r *http.Request) {
	f, _ := s.store.Snapshot()
	name := r.PathValue("name")
	fl, ok := f.Flags[name]
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Sprintf("unknown flag %q", name))
		return
	}
	tag := etagFor("flag", f.Hash, name)
	if writeETag(w, r, tag) {
		return
	}
	writeJSON(w, http.StatusOK, fl)
}

func (s *Server) handleEvalOne(w http.ResponseWriter, r *http.Request) {
	f, _ := s.store.Snapshot()
	name := r.PathValue("name")
	fl, ok := f.Flags[name]
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Sprintf("unknown flag %q", name))
		return
	}
	ctx := contextFromQuery(r)
	tag := etagFor("eval", f.Hash, name, canonicalContext(ctx))
	if writeETag(w, r, tag) {
		return
	}
	writeJSON(w, http.StatusOK, eval.Evaluate(fl, ctx))
}

// evalRequest is the POST /v1/eval body.
type evalRequest struct {
	Key        string            `json:"key"`
	Attributes map[string]string `json:"attributes"`
	Flags      []string          `json:"flags"` // empty = every flag
}

func (s *Server) handleEvalBatch(w http.ResponseWriter, r *http.Request) {
	f, _ := s.store.Snapshot()
	var req evalRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	ctx := eval.Context{Key: req.Key, Attributes: req.Attributes}
	results, err := eval.EvaluateAll(f, ctx, req.Flags)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"key":     req.Key,
		"hash":    f.Hash,
		"results": results,
	})
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	f, _ := s.store.Snapshot()
	tag := etagFor("config", f.Hash)
	if writeETag(w, r, tag) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"hash":   f.Hash,
		"config": f.Config,
	})
}

func (s *Server) handleConfigPath(w http.ResponseWriter, r *http.Request) {
	f, _ := s.store.Snapshot()
	path := strings.Trim(r.PathValue("path"), "/")
	val, ok := configLookup(f.Config, path)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Sprintf("unknown config path %q", path))
		return
	}
	tag := etagFor("configpath", f.Hash, path)
	if writeETag(w, r, tag) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": path, "value": val})
}

// configLookup walks the config tree by '/'-separated segments.
func configLookup(tree map[string]any, path string) (any, bool) {
	if path == "" {
		return nil, false
	}
	var cur any = tree
	for _, seg := range strings.Split(path, "/") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = m[seg]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

// --- request/response plumbing ----------------------------------------------

// contextFromQuery builds an eval context from ?key=…&attr.country=JP&…
func contextFromQuery(r *http.Request) eval.Context {
	q := r.URL.Query()
	ctx := eval.Context{Key: q.Get("key")}
	for param, vals := range q {
		name, ok := strings.CutPrefix(param, "attr.")
		if !ok || name == "" || len(vals) == 0 {
			continue
		}
		if ctx.Attributes == nil {
			ctx.Attributes = map[string]string{}
		}
		ctx.Attributes[name] = vals[0]
	}
	return ctx
}

// canonicalContext renders a context deterministically for ETag input.
func canonicalContext(ctx eval.Context) string {
	keys := make([]string, 0, len(ctx.Attributes))
	for k := range ctx.Attributes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(ctx.Key)
	for _, k := range keys {
		b.WriteByte(0)
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(ctx.Attributes[k])
	}
	return b.String()
}

// etagFor derives a strong ETag from its parts (always including the
// file hash, so any file change invalidates every cached response).
func etagFor(parts ...string) string {
	h := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return `"` + hex.EncodeToString(h[:16]) + `"`
}

// writeETag sets caching headers and answers 304 when the client's
// If-None-Match already names the current representation. Returns true
// when the response is complete.
func writeETag(w http.ResponseWriter, r *http.Request, tag string) bool {
	w.Header().Set("ETag", tag)
	w.Header().Set("Cache-Control", "no-cache") // revalidate every time
	inm := r.Header.Get("If-None-Match")
	if inm == "" {
		return false
	}
	for _, part := range strings.Split(inm, ",") {
		part = strings.TrimSpace(part)
		part = strings.TrimPrefix(part, "W/")
		if part == tag || part == "*" {
			w.WriteHeader(http.StatusNotModified)
			return true
		}
	}
	return false
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
