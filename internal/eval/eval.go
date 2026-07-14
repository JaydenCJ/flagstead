// Package eval decides a flag for one evaluation context. The semantics
// are deliberately small and fully documented in docs/file-format.md:
//
//  1. A disabled flag is off for everyone, no exceptions.
//  2. Rules run in file order; the FIRST matching rule decides — it can
//     force off, force a variant, or override the rollout percentage.
//  3. Whatever decided "on", the (possibly overridden) percent rollout
//     gates the final answer via deterministic bucketing.
//  4. When the flag carries variants, an enabled result also picks one
//     arm by weight, using an independent hash so the rollout population
//     is not correlated with the variant split.
//
// Evaluation is a pure function of (flag definition, context) — same
// inputs, same answer, on every machine, forever.
package eval

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/JaydenCJ/flagstead/internal/flagfile"
	"github.com/JaydenCJ/flagstead/internal/rollout"
)

// Context is who/what a flag is evaluated for.
type Context struct {
	Key        string            `json:"key"`
	Attributes map[string]string `json:"attributes,omitempty"`
}

// Result is one flag decision, with enough detail to debug it.
type Result struct {
	Flag      string `json:"flag"`
	Key       string `json:"key"`
	Enabled   bool   `json:"enabled"`
	Variant   string `json:"variant,omitempty"`
	Reason    string `json:"reason"`
	RuleIndex int    `json:"rule_index"` // -1 when no rule matched
	Bucket    int    `json:"bucket"`     // -1 when bucketing was not consulted
}

// Reasons attached to results.
const (
	ReasonFlagDisabled = "flag_disabled" // the flag itself is enabled = false
	ReasonRule         = "rule"          // a targeting rule decided
	ReasonRollout      = "rollout"       // the percent gate decided
	ReasonDefault      = "default"       // enabled with rollout = 100, no rule
)

// Evaluate decides flag f for ctx.
func Evaluate(f *flagfile.Flag, ctx Context) Result {
	res := Result{Flag: f.Name, Key: ctx.Key, RuleIndex: -1, Bucket: -1}
	if !f.Enabled {
		res.Reason = ReasonFlagDisabled
		return res
	}

	for i := range f.Rules {
		r := &f.Rules[i]
		if !ruleMatches(r, ctx.Attributes) {
			continue
		}
		res.RuleIndex = i
		if r.Enabled != nil && !*r.Enabled {
			res.Reason = ReasonRule
			return res
		}
		percent := f.Rollout
		if r.Rollout != nil {
			percent = *r.Rollout
		}
		if percent < 100 {
			res.Bucket = rollout.Bucket(f.Salt, ctx.Key)
			if !rollout.InRollout(f.Salt, ctx.Key, percent) {
				res.Reason = ReasonRollout
				return res
			}
		}
		res.Enabled = true
		res.Reason = ReasonRule
		if r.Variant != "" {
			res.Variant = r.Variant
		} else {
			res.Variant = weightedVariant(f, ctx.Key)
		}
		return res
	}

	if f.Rollout < 100 {
		res.Bucket = rollout.Bucket(f.Salt, ctx.Key)
		res.Reason = ReasonRollout
		if !rollout.InRollout(f.Salt, ctx.Key, f.Rollout) {
			return res
		}
	} else {
		res.Reason = ReasonDefault
	}
	res.Enabled = true
	res.Variant = weightedVariant(f, ctx.Key)
	return res
}

// EvaluateAll evaluates the named flags (all of them, sorted, when names
// is empty) and errors on the first unknown name.
func EvaluateAll(f *flagfile.File, ctx Context, names []string) ([]Result, error) {
	if len(names) == 0 {
		names = f.FlagNames()
	}
	out := make([]Result, 0, len(names))
	for _, n := range names {
		fl, ok := f.Flags[n]
		if !ok {
			return nil, fmt.Errorf("unknown flag %q", n)
		}
		out = append(out, Evaluate(fl, ctx))
	}
	return out, nil
}

// weightedVariant picks an arm proportionally to weight. The hash salt is
// derived from the flag salt but distinct from the rollout salt, so being
// in the first 10% of a rollout says nothing about which variant you get.
func weightedVariant(f *flagfile.Flag, key string) string {
	if len(f.Variants) == 0 {
		return ""
	}
	total := 0.0
	for _, v := range f.Variants {
		total += v.Weight
	}
	point := float64(rollout.Bucket(f.Salt+"\x00variant", key)) / rollout.Buckets * total
	acc := 0.0
	for _, v := range f.Variants {
		acc += v.Weight
		if point < acc {
			return v.Name
		}
	}
	return f.Variants[len(f.Variants)-1].Name
}

// ruleMatches reports whether one rule matches the attributes. A missing
// attribute only matches the not_exists operator — never ne or not_in —
// so absent data fails closed instead of accidentally targeting everyone.
func ruleMatches(r *flagfile.Rule, attrs map[string]string) bool {
	val, present := attrs[r.Attribute]
	switch r.Op {
	case "exists":
		return present
	case "not_exists":
		return !present
	}
	if !present {
		return false
	}
	switch r.Op {
	case "eq":
		return val == r.Values[0]
	case "ne":
		return val != r.Values[0]
	case "in":
		return contains(r.Values, val)
	case "not_in":
		return !contains(r.Values, val)
	case "contains":
		return strings.Contains(val, r.Values[0])
	case "prefix":
		return strings.HasPrefix(val, r.Values[0])
	case "suffix":
		return strings.HasSuffix(val, r.Values[0])
	case "gt", "gte", "lt", "lte":
		return numericCompare(r.Op, val, r.Values[0])
	}
	return false
}

// numericCompare parses both sides as floats; a non-numeric attribute
// value simply does not match (rules never error at evaluation time).
func numericCompare(op, attr, want string) bool {
	a, errA := strconv.ParseFloat(attr, 64)
	b, errB := strconv.ParseFloat(want, 64)
	if errA != nil || errB != nil {
		return false
	}
	switch op {
	case "gt":
		return a > b
	case "gte":
		return a >= b
	case "lt":
		return a < b
	case "lte":
		return a <= b
	}
	return false
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
