// Package flagfile turns a parsed TOML document into flagstead's typed
// model: named boolean flags with percent rollouts, targeting rules and
// weighted variants, plus a free-form remote-config tree. Validation is
// strict — unknown keys, bad types and out-of-range percentages are all
// reported (with the full path context, and all at once) rather than
// silently ignored, because a typo in a flag file must never flip a flag
// in production.
package flagfile

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/JaydenCJ/flagstead/internal/toml"
)

// File is one fully validated flag file.
type File struct {
	Version int              // file format version; always 1 today
	Flags   map[string]*Flag // keyed by flag name
	Config  map[string]any   // free-form remote-config tree
	Hash    string           // hex SHA-256 of the raw file bytes
}

// Flag is a single feature flag.
type Flag struct {
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Enabled     bool      `json:"enabled"`
	Rollout     float64   `json:"rollout"` // percent of keys, 0–100
	Salt        string    `json:"salt"`    // bucketing salt, defaults to the name
	Tags        []string  `json:"tags,omitempty"`
	Rules       []Rule    `json:"rules,omitempty"`
	Variants    []Variant `json:"variants,omitempty"`
}

// Rule is one targeting rule; the first matching rule decides the outcome.
type Rule struct {
	Attribute string   `json:"attribute"`
	Op        string   `json:"op"`
	Values    []string `json:"values,omitempty"`
	Enabled   *bool    `json:"enabled,omitempty"` // outcome override; nil means "enabled"
	Variant   string   `json:"variant,omitempty"` // forced variant on match
	Rollout   *float64 `json:"rollout,omitempty"` // per-rule rollout percent
}

// Variant is one weighted arm of an experiment.
type Variant struct {
	Name   string  `json:"name"`
	Weight float64 `json:"weight"`
}

// Rule operators, grouped by how many values they take.
var (
	singleValueOps = map[string]bool{
		"eq": true, "ne": true, "contains": true, "prefix": true,
		"suffix": true, "gt": true, "gte": true, "lt": true, "lte": true,
	}
	multiValueOps = map[string]bool{"in": true, "not_in": true}
	zeroValueOps  = map[string]bool{"exists": true, "not_exists": true}
)

// OpNames lists every supported rule operator, sorted.
func OpNames() []string {
	names := make([]string, 0, len(singleValueOps)+len(multiValueOps)+len(zeroValueOps))
	for op := range singleValueOps {
		names = append(names, op)
	}
	for op := range multiValueOps {
		names = append(names, op)
	}
	for op := range zeroValueOps {
		names = append(names, op)
	}
	sort.Strings(names)
	return names
}

// ValidationError aggregates every problem found in one pass so users fix
// a file in one round trip instead of playing error whack-a-mole.
type ValidationError struct {
	Problems []string
}

func (e *ValidationError) Error() string { return strings.Join(e.Problems, "\n") }

// Parse validates src and returns the typed file. The returned error is a
// *toml.ParseError for syntax problems or a *ValidationError listing every
// semantic problem found.
func Parse(src []byte) (*File, error) {
	doc, err := toml.Parse(src)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(src)
	f := &File{
		Version: 1,
		Flags:   map[string]*Flag{},
		Config:  map[string]any{},
		Hash:    hex.EncodeToString(sum[:]),
	}
	v := &validator{}

	for _, key := range sortedKeys(doc) {
		switch key {
		case "version", "flags", "config":
		default:
			v.errf("unknown top-level key %q (expected version, flags or config)", key)
		}
	}
	if raw, ok := doc["version"]; ok {
		n, isInt := raw.(int64)
		if !isInt || n != 1 {
			v.errf("version: must be the integer 1, got %v", raw)
		}
	}
	if raw, ok := doc["flags"]; ok {
		tbl, isTable := raw.(map[string]any)
		if !isTable {
			v.errf("flags: must be a table of flag definitions")
		} else {
			for _, name := range sortedKeys(tbl) {
				if fl := v.flag(name, tbl[name]); fl != nil {
					f.Flags[name] = fl
				}
			}
		}
	}
	if raw, ok := doc["config"]; ok {
		tbl, isTable := raw.(map[string]any)
		if !isTable {
			v.errf("config: must be a table")
		} else {
			f.Config = tbl
		}
	}

	if len(v.problems) > 0 {
		return nil, &ValidationError{Problems: v.problems}
	}
	return f, nil
}

// FlagNames returns all flag names, sorted, for deterministic output.
func (f *File) FlagNames() []string {
	names := make([]string, 0, len(f.Flags))
	for n := range f.Flags {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// --- validation -------------------------------------------------------------

type validator struct {
	problems []string
}

func (v *validator) errf(format string, args ...any) {
	v.problems = append(v.problems, fmt.Sprintf(format, args...))
}

func validFlagName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' ||
			c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-' {
			continue
		}
		return false
	}
	return true
}

func (v *validator) flag(name string, raw any) *Flag {
	path := "flags." + name
	if !validFlagName(name) {
		v.errf("%s: flag names may only contain letters, digits, '.', '_' and '-'", path)
		return nil
	}
	tbl, ok := raw.(map[string]any)
	if !ok {
		v.errf("%s: must be a table", path)
		return nil
	}

	fl := &Flag{Name: name, Rollout: 100, Salt: name}
	before := len(v.problems)

	for _, key := range sortedKeys(tbl) {
		val := tbl[key]
		switch key {
		case "description":
			fl.Description = v.str(path+".description", val)
		case "enabled":
			b, isBool := val.(bool)
			if !isBool {
				v.errf("%s.enabled: must be a boolean", path)
			}
			fl.Enabled = b
		case "rollout":
			fl.Rollout = v.percent(path+".rollout", val)
		case "salt":
			if s := v.str(path+".salt", val); s != "" {
				fl.Salt = s
			}
		case "tags":
			fl.Tags = v.strSlice(path+".tags", val)
		case "variants":
			fl.Variants = v.variants(path, val)
		case "rules":
			// validated after variants so rules can reference them
		default:
			v.errf("%s: unknown key %q", path, key)
		}
	}
	if _, ok := tbl["enabled"]; !ok {
		v.errf("%s: missing required key \"enabled\" (flags must be explicit)", path)
	}
	if raw, ok := tbl["rules"]; ok {
		fl.Rules = v.rules(path, raw, fl.Variants)
	}

	if len(v.problems) > before {
		return nil
	}
	return fl
}

func (v *validator) variants(path string, raw any) []Variant {
	arr, ok := raw.([]any)
	if !ok {
		v.errf("%s.variants: must be an array of tables ([[flags.<name>.variants]])", path)
		return nil
	}
	var out []Variant
	seen := map[string]bool{}
	total := 0.0
	for i, item := range arr {
		vp := fmt.Sprintf("%s.variants[%d]", path, i)
		tbl, isTable := item.(map[string]any)
		if !isTable {
			v.errf("%s: must be a table", vp)
			continue
		}
		var vr Variant
		for _, key := range sortedKeys(tbl) {
			switch key {
			case "name":
				vr.Name = v.str(vp+".name", tbl[key])
			case "weight":
				w, isNum := number(tbl[key])
				if !isNum || w < 0 {
					v.errf("%s.weight: must be a non-negative number", vp)
				} else {
					vr.Weight = w
				}
			default:
				v.errf("%s: unknown key %q", vp, key)
			}
		}
		if vr.Name == "" {
			v.errf("%s: missing required key \"name\"", vp)
			continue
		}
		if seen[vr.Name] {
			v.errf("%s: duplicate variant name %q", vp, vr.Name)
			continue
		}
		seen[vr.Name] = true
		total += vr.Weight
		out = append(out, vr)
	}
	if len(out) > 0 && total <= 0 {
		v.errf("%s.variants: total weight must be greater than zero", path)
	}
	return out
}

func (v *validator) rules(path string, raw any, variants []Variant) []Rule {
	arr, ok := raw.([]any)
	if !ok {
		v.errf("%s.rules: must be an array of tables ([[flags.<name>.rules]])", path)
		return nil
	}
	variantNames := map[string]bool{}
	for _, vr := range variants {
		variantNames[vr.Name] = true
	}
	var out []Rule
	for i, item := range arr {
		rp := fmt.Sprintf("%s.rules[%d]", path, i)
		tbl, isTable := item.(map[string]any)
		if !isTable {
			v.errf("%s: must be a table", rp)
			continue
		}
		var r Rule
		for _, key := range sortedKeys(tbl) {
			val := tbl[key]
			switch key {
			case "attribute":
				r.Attribute = v.str(rp+".attribute", val)
			case "op":
				r.Op = v.str(rp+".op", val)
			case "values":
				r.Values = v.strSlice(rp+".values", val)
			case "value":
				if s, isStr := val.(string); isStr {
					r.Values = append(r.Values, s)
				} else {
					v.errf("%s.value: must be a string", rp)
				}
			case "enabled":
				b, isBool := val.(bool)
				if !isBool {
					v.errf("%s.enabled: must be a boolean", rp)
				} else {
					r.Enabled = &b
				}
			case "variant":
				r.Variant = v.str(rp+".variant", val)
			case "rollout":
				pct := v.percent(rp+".rollout", val)
				r.Rollout = &pct
			default:
				v.errf("%s: unknown key %q", rp, key)
			}
		}
		if _, ok := tbl["values"]; ok {
			if _, both := tbl["value"]; both {
				v.errf("%s: set either \"value\" or \"values\", not both", rp)
			}
		}
		if r.Attribute == "" {
			v.errf("%s: missing required key \"attribute\"", rp)
		}
		v.ruleOp(rp, &r)
		if r.Variant != "" && !variantNames[r.Variant] {
			v.errf("%s.variant: %q is not a declared variant of this flag", rp, r.Variant)
		}
		out = append(out, r)
	}
	return out
}

func (v *validator) ruleOp(rp string, r *Rule) {
	switch {
	case r.Op == "":
		v.errf("%s: missing required key \"op\"", rp)
	case singleValueOps[r.Op]:
		if len(r.Values) != 1 {
			v.errf("%s: op %q takes exactly one value, got %d", rp, r.Op, len(r.Values))
		}
	case multiValueOps[r.Op]:
		if len(r.Values) == 0 {
			v.errf("%s: op %q needs at least one value", rp, r.Op)
		}
	case zeroValueOps[r.Op]:
		if len(r.Values) != 0 {
			v.errf("%s: op %q takes no values", rp, r.Op)
		}
	default:
		v.errf("%s: unknown op %q (supported: %s)", rp, r.Op, strings.Join(OpNames(), ", "))
	}
}

func (v *validator) str(path string, raw any) string {
	s, ok := raw.(string)
	if !ok {
		v.errf("%s: must be a string", path)
		return ""
	}
	return s
}

func (v *validator) strSlice(path string, raw any) []string {
	arr, ok := raw.([]any)
	if !ok {
		v.errf("%s: must be an array of strings", path)
		return nil
	}
	out := make([]string, 0, len(arr))
	for i, item := range arr {
		s, isStr := item.(string)
		if !isStr {
			v.errf("%s[%d]: must be a string", path, i)
			continue
		}
		out = append(out, s)
	}
	return out
}

func (v *validator) percent(path string, raw any) float64 {
	n, ok := number(raw)
	if !ok {
		v.errf("%s: must be a number between 0 and 100", path)
		return 0
	}
	if n < 0 || n > 100 {
		v.errf("%s: must be between 0 and 100, got %v", path, raw)
		return 0
	}
	return n
}

func number(raw any) (float64, bool) {
	switch n := raw.(type) {
	case int64:
		return float64(n), true
	case float64:
		return n, true
	}
	return 0, false
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
