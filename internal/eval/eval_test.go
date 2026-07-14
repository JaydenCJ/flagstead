// Tests for evaluation semantics: rule precedence, rollout gating,
// variant assignment, and the fail-closed handling of missing attributes.
// Keys with known bucket positions are found by scanning, not hardcoded,
// so the tests stay valid if sample sizes change — but everything is
// still fully deterministic (FNV-1a, no randomness).
package eval

import (
	"fmt"
	"testing"

	"github.com/JaydenCJ/flagstead/internal/flagfile"
	"github.com/JaydenCJ/flagstead/internal/rollout"
)

func boolPtr(b bool) *bool      { return &b }
func pctPtr(p float64) *float64 { return &p }
func attrs(kv ...string) map[string]string {
	m := map[string]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return m
}

// keyWithBucket scans deterministic candidate keys until one lands in
// [lo, hi) for the given salt.
func keyWithBucket(t *testing.T, salt string, lo, hi int) string {
	t.Helper()
	for i := 0; i < 100000; i++ {
		key := fmt.Sprintf("probe-%d", i)
		if b := rollout.Bucket(salt, key); b >= lo && b < hi {
			return key
		}
	}
	t.Fatalf("no key found with bucket in [%d,%d) for salt %q", lo, hi, salt)
	return ""
}

func newFlag(name string) *flagfile.Flag {
	return &flagfile.Flag{Name: name, Enabled: true, Rollout: 100, Salt: name}
}

func TestDisabledFlagIsOffForEveryone(t *testing.T) {
	f := newFlag("kill-switch")
	f.Enabled = false
	f.Rules = []flagfile.Rule{{Attribute: "role", Op: "eq", Values: []string{"admin"}}}
	res := Evaluate(f, Context{Key: "u1", Attributes: attrs("role", "admin")})
	if res.Enabled || res.Reason != ReasonFlagDisabled {
		t.Fatalf("disabled flag leaked through: %+v", res)
	}
}

func TestEnabledFullRolloutReasonDefault(t *testing.T) {
	res := Evaluate(newFlag("simple"), Context{Key: "u1"})
	if !res.Enabled || res.Reason != ReasonDefault || res.Bucket != -1 {
		t.Fatalf("unexpected result: %+v", res)
	}
}

func TestPercentRolloutGatesByBucket(t *testing.T) {
	f := newFlag("gated")
	f.Rollout = 25 // buckets 0..2499 are in
	inKey := keyWithBucket(t, "gated", 0, 2500)
	outKey := keyWithBucket(t, "gated", 2500, rollout.Buckets)

	in := Evaluate(f, Context{Key: inKey})
	if !in.Enabled || in.Reason != ReasonRollout || in.Bucket >= 2500 {
		t.Fatalf("in-rollout key wrong: %+v", in)
	}
	out := Evaluate(f, Context{Key: outKey})
	if out.Enabled || out.Reason != ReasonRollout || out.Bucket < 2500 {
		t.Fatalf("out-of-rollout key wrong: %+v", out)
	}
}

func TestEvaluationIsDeterministicAcrossCalls(t *testing.T) {
	f := newFlag("stable")
	f.Rollout = 50
	first := Evaluate(f, Context{Key: "user-7"})
	for i := 0; i < 100; i++ {
		if Evaluate(f, Context{Key: "user-7"}) != first {
			t.Fatal("evaluation is not deterministic")
		}
	}
	// An empty key is unusual but must be just as deterministic.
	if Evaluate(f, Context{}) != Evaluate(f, Context{}) {
		t.Fatal("empty-key evaluation must still be deterministic")
	}
}

func TestFirstMatchingRuleWins(t *testing.T) {
	f := newFlag("layered")
	f.Rules = []flagfile.Rule{
		{Attribute: "country", Op: "eq", Values: []string{"JP"}, Enabled: boolPtr(false)},
		{Attribute: "country", Op: "exists", Enabled: boolPtr(true)},
	}
	res := Evaluate(f, Context{Key: "u", Attributes: attrs("country", "JP")})
	if res.Enabled || res.RuleIndex != 0 || res.Reason != ReasonRule {
		t.Fatalf("first rule should win: %+v", res)
	}
	res = Evaluate(f, Context{Key: "u", Attributes: attrs("country", "DE")})
	if !res.Enabled || res.RuleIndex != 1 {
		t.Fatalf("second rule should match DE: %+v", res)
	}
}

func TestRuleRolloutOverridesFlagRollout(t *testing.T) {
	f := newFlag("override")
	f.Rollout = 0 // nobody by default
	f.Rules = []flagfile.Rule{
		{Attribute: "beta", Op: "eq", Values: []string{"yes"}, Rollout: pctPtr(100)},
	}
	res := Evaluate(f, Context{Key: "u", Attributes: attrs("beta", "yes")})
	if !res.Enabled || res.Reason != ReasonRule {
		t.Fatalf("rule rollout=100 should bypass flag rollout=0: %+v", res)
	}
	// A non-matching context still faces the 0% default.
	res = Evaluate(f, Context{Key: "u"})
	if res.Enabled {
		t.Fatalf("non-matching context must stay gated: %+v", res)
	}
}

func TestRuleMatchStillSubjectToRolloutGate(t *testing.T) {
	f := newFlag("partial")
	f.Rollout = 25
	f.Rules = []flagfile.Rule{{Attribute: "tier", Op: "eq", Values: []string{"pro"}}}
	outKey := keyWithBucket(t, "partial", 2500, rollout.Buckets)
	res := Evaluate(f, Context{Key: outKey, Attributes: attrs("tier", "pro")})
	if res.Enabled || res.Reason != ReasonRollout || res.RuleIndex != 0 {
		t.Fatalf("matched rule must still respect the percent gate: %+v", res)
	}
}

func TestRuleForcedVariant(t *testing.T) {
	f := newFlag("exp")
	f.Variants = []flagfile.Variant{{Name: "a", Weight: 1}, {Name: "b", Weight: 1}}
	f.Rules = []flagfile.Rule{
		{Attribute: "qa", Op: "eq", Values: []string{"1"}, Variant: "b"},
	}
	res := Evaluate(f, Context{Key: "any", Attributes: attrs("qa", "1")})
	if !res.Enabled || res.Variant != "b" {
		t.Fatalf("forced variant lost: %+v", res)
	}
}

func TestStringOperators(t *testing.T) {
	cases := []struct {
		op    string
		vals  []string
		attr  string
		match bool
	}{
		{"eq", []string{"JP"}, "JP", true},
		{"eq", []string{"JP"}, "DE", false},
		{"ne", []string{"JP"}, "DE", true},
		{"in", []string{"JP", "DE"}, "DE", true},
		{"in", []string{"JP", "DE"}, "FR", false},
		{"not_in", []string{"JP", "DE"}, "FR", true},
		{"contains", []string{"beta"}, "beta-tester", true},
		{"prefix", []string{"org-"}, "org-42", true},
		{"prefix", []string{"org-"}, "user-42", false},
		{"suffix", []string{".test"}, "api.example.test", true},
	}
	for _, tc := range cases {
		f := newFlag("ops")
		f.Rules = []flagfile.Rule{{Attribute: "a", Op: tc.op, Values: tc.vals}}
		res := Evaluate(f, Context{Key: "k", Attributes: attrs("a", tc.attr)})
		matched := res.RuleIndex == 0
		if matched != tc.match {
			t.Errorf("op %s values %v attr %q: matched=%v, want %v",
				tc.op, tc.vals, tc.attr, matched, tc.match)
		}
	}
}

func TestNumericOperatorsCompareAsNumbers(t *testing.T) {
	f := newFlag("num")
	f.Rules = []flagfile.Rule{{Attribute: "v", Op: "gte", Values: []string{"10"}}}
	// String comparison would say "9" >= "10"; numeric must not.
	res := Evaluate(f, Context{Key: "k", Attributes: attrs("v", "9")})
	if res.RuleIndex == 0 {
		t.Fatalf("9 >= 10 matched — comparing as strings? %+v", res)
	}
	res = Evaluate(f, Context{Key: "k", Attributes: attrs("v", "10.5")})
	if res.RuleIndex != 0 {
		t.Fatalf("10.5 >= 10 should match: %+v", res)
	}
	// A non-numeric attribute value must simply not match, never error.
	f.Rules = []flagfile.Rule{{Attribute: "v", Op: "lt", Values: []string{"5"}}}
	res = Evaluate(f, Context{Key: "k", Attributes: attrs("v", "banana")})
	if res.RuleIndex == 0 {
		t.Fatalf("non-numeric attribute must not match a numeric op: %+v", res)
	}
}

func TestExistsAndNotExists(t *testing.T) {
	f := newFlag("presence")
	f.Rules = []flagfile.Rule{{Attribute: "email", Op: "exists"}}
	if Evaluate(f, Context{Key: "k"}).RuleIndex == 0 {
		t.Fatal("exists matched a missing attribute")
	}
	if Evaluate(f, Context{Key: "k", Attributes: attrs("email", "")}).RuleIndex != 0 {
		t.Fatal("exists should match a present-but-empty attribute")
	}
	f.Rules = []flagfile.Rule{{Attribute: "email", Op: "not_exists"}}
	if Evaluate(f, Context{Key: "k"}).RuleIndex != 0 {
		t.Fatal("not_exists should match a missing attribute")
	}
}

func TestMissingAttributeFailsClosedForNegativeOps(t *testing.T) {
	// ne / not_in are the dangerous ones: with a missing attribute they
	// must NOT match, or a typo'd attribute name targets everyone.
	for _, op := range []string{"ne", "not_in"} {
		f := newFlag("closed")
		f.Rollout = 0
		f.Rules = []flagfile.Rule{{Attribute: "country", Op: op, Values: []string{"JP"}, Rollout: pctPtr(100)}}
		res := Evaluate(f, Context{Key: "k"})
		if res.Enabled {
			t.Fatalf("op %s matched on a missing attribute: %+v", op, res)
		}
	}
}

func TestWeightedVariantsAreDeterministicAndProportional(t *testing.T) {
	f := newFlag("abtest")
	f.Variants = []flagfile.Variant{
		{Name: "control", Weight: 75},
		{Name: "treatment", Weight: 25},
	}
	counts := map[string]int{}
	const n = 8000
	for i := 0; i < n; i++ {
		key := fmt.Sprintf("user-%d", i)
		v1 := Evaluate(f, Context{Key: key}).Variant
		v2 := Evaluate(f, Context{Key: key}).Variant
		if v1 != v2 {
			t.Fatalf("variant flapped for %s: %s vs %s", key, v1, v2)
		}
		counts[v1]++
	}
	share := float64(counts["control"]) / n * 100
	if share < 72 || share > 78 {
		t.Fatalf("control share %.1f%% outside 75±3", share)
	}

	// A zero-weight arm (a paused variant) must never be picked.
	f.Variants = append(f.Variants, flagfile.Variant{Name: "paused", Weight: 0})
	for i := 0; i < 2000; i++ {
		if v := Evaluate(f, Context{Key: fmt.Sprintf("u%d", i)}).Variant; v == "paused" {
			t.Fatal("zero-weight variant was picked")
		}
	}
}

func TestVariantSplitIndependentOfRolloutPopulation(t *testing.T) {
	// Users inside a 20% rollout must still split ~50/50 across variants;
	// if the same hash gated both, the rollout survivors would all get
	// the same arm.
	f := newFlag("indep")
	f.Rollout = 20
	f.Variants = []flagfile.Variant{{Name: "a", Weight: 50}, {Name: "b", Weight: 50}}
	counts := map[string]int{}
	for i := 0; i < 20000; i++ {
		res := Evaluate(f, Context{Key: fmt.Sprintf("user-%d", i)})
		if res.Enabled {
			counts[res.Variant]++
		}
	}
	total := counts["a"] + counts["b"]
	if total == 0 {
		t.Fatal("rollout enabled nobody")
	}
	share := float64(counts["a"]) / float64(total) * 100
	if share < 44 || share > 56 {
		t.Fatalf("variant split %.1f%% correlates with rollout hash", share)
	}
}

func TestEvaluateAllDefaultsToEveryFlagSorted(t *testing.T) {
	file := &flagfile.File{Flags: map[string]*flagfile.Flag{
		"zeta":  newFlag("zeta"),
		"alpha": newFlag("alpha"),
	}}
	results, err := EvaluateAll(file, Context{Key: "u"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].Flag != "alpha" || results[1].Flag != "zeta" {
		t.Fatalf("want sorted alpha,zeta — got %+v", results)
	}
}

func TestEvaluateAllRejectsUnknownFlag(t *testing.T) {
	file := &flagfile.File{Flags: map[string]*flagfile.Flag{"a": newFlag("a")}}
	_, err := EvaluateAll(file, Context{}, []string{"a", "ghost"})
	if err == nil || err.Error() != `unknown flag "ghost"` {
		t.Fatalf("want unknown-flag error, got %v", err)
	}
}
