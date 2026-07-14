// Tests for flag-file validation. The strictness table matters most: a
// typo like `enbled = true` or `rollout = 250` must fail `flagstead
// check` loudly rather than ship a mis-set flag.
package flagfile

import (
	"reflect"
	"strings"
	"testing"
)

const minimalFlag = "[flags.demo]\nenabled = true\n"

func mustParse(t *testing.T, src string) *File {
	t.Helper()
	f, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	return f
}

// mustFailWith asserts that parsing fails and the error mentions want.
func mustFailWith(t *testing.T, src, want string) {
	t.Helper()
	_, err := Parse([]byte(src))
	if err == nil {
		t.Fatalf("Parse(%q) unexpectedly succeeded", src)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error %q should contain %q", err.Error(), want)
	}
}

func TestParseMinimalFlagAppliesDefaults(t *testing.T) {
	f := mustParse(t, minimalFlag)
	fl := f.Flags["demo"]
	if fl == nil {
		t.Fatal("flag missing")
	}
	if !fl.Enabled || fl.Rollout != 100 || fl.Salt != "demo" {
		t.Fatalf("defaults wrong: %+v", fl)
	}
}

func TestParseFullFlagDefinition(t *testing.T) {
	src := `version = 1
[flags.checkout]
description = "New checkout"
enabled = true
rollout = 25.5
salt = "checkout-v2"
tags = ["web", "q3"]

[[flags.checkout.variants]]
name = "control"
weight = 50

[[flags.checkout.variants]]
name = "treatment"
weight = 50

[[flags.checkout.rules]]
attribute = "country"
op = "in"
values = ["JP", "DE"]

[flags.gate]
enabled = true
rollout = 25
`
	f := mustParse(t, src)
	fl := f.Flags["checkout"]
	if fl.Description != "New checkout" || fl.Rollout != 25.5 || fl.Salt != "checkout-v2" {
		t.Fatalf("fields wrong: %+v", fl)
	}
	if !reflect.DeepEqual(fl.Tags, []string{"web", "q3"}) {
		t.Fatalf("tags wrong: %v", fl.Tags)
	}
	if len(fl.Variants) != 2 || fl.Variants[0].Name != "control" {
		t.Fatalf("variants wrong: %v", fl.Variants)
	}
	if len(fl.Rules) != 1 || fl.Rules[0].Op != "in" {
		t.Fatalf("rules wrong: %v", fl.Rules)
	}
	if f.Flags["gate"].Rollout != 25 {
		t.Fatalf("integer rollout wrong: %v", f.Flags["gate"].Rollout)
	}
}

// TestParseStrictnessRejections: every silently-wrong-flag hazard must be
// a hard validation error with an actionable message.
func TestParseStrictnessRejections(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{"missing enabled", "[flags.demo]\nrollout = 50\n", `missing required key "enabled"`},
		{"unknown top-level key", "flagz = 1\n" + minimalFlag, "unknown top-level key"},
		{"typo'd flag key", "[flags.demo]\nenabled = true\nenbled = true\n", `unknown key "enbled"`},
		{"wrong version", "version = 2\n" + minimalFlag, "must be the integer 1"},
		{"rollout too high", "[flags.demo]\nenabled = true\nrollout = 250\n", "between 0 and 100"},
		{"rollout negative", "[flags.demo]\nenabled = true\nrollout = -1\n", "between 0 and 100"},
		{"enabled not boolean", "[flags.demo]\nenabled = \"yes\"\n", "must be a boolean"},
		{"bad flag name", "[flags.\"has space\"]\nenabled = true\n", "flag names may only contain"},
		{"non-string tag", "[flags.demo]\nenabled = true\ntags = [1, 2]\n", "must be a string"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { mustFailWith(t, tc.src, tc.want) })
	}
}

func TestParseRuleUnknownOpListsSupportedOps(t *testing.T) {
	src := minimalFlag + "[[flags.demo.rules]]\nattribute = \"x\"\nop = \"matches\"\nvalue = \"y\"\n"
	mustFailWith(t, src, "unknown op")
	mustFailWith(t, src, "not_exists") // the message enumerates all ops
}

func TestParseRuleShapeValidation(t *testing.T) {
	base := minimalFlag + "[[flags.demo.rules]]\nattribute = \"x\"\n"
	mustFailWith(t, base+"op = \"eq\"\nvalues = [\"a\", \"b\"]\n", "exactly one value")
	mustFailWith(t, base+"op = \"in\"\n", "at least one value")
	mustFailWith(t, base+"op = \"exists\"\nvalue = \"a\"\n", "takes no values")
	mustFailWith(t, base+"op = \"eq\"\nvalue = \"a\"\nvalues = [\"b\"]\n", "not both")
	mustFailWith(t, minimalFlag+"[[flags.demo.rules]]\nop = \"exists\"\n",
		`missing required key "attribute"`)
}

func TestParseRuleValueIsSugarForSingleValues(t *testing.T) {
	src := minimalFlag + "[[flags.demo.rules]]\nattribute = \"x\"\nop = \"eq\"\nvalue = \"a\"\n"
	f := mustParse(t, src)
	if !reflect.DeepEqual(f.Flags["demo"].Rules[0].Values, []string{"a"}) {
		t.Fatalf("value sugar broken: %v", f.Flags["demo"].Rules[0].Values)
	}
}

func TestParseRuleVariantMustBeDeclared(t *testing.T) {
	src := minimalFlag +
		"[[flags.demo.rules]]\nattribute = \"x\"\nop = \"exists\"\nvariant = \"ghost\"\n"
	mustFailWith(t, src, "not a declared variant")
}

func TestParseVariantValidation(t *testing.T) {
	dup := minimalFlag +
		"[[flags.demo.variants]]\nname = \"a\"\nweight = 1\n" +
		"[[flags.demo.variants]]\nname = \"a\"\nweight = 1\n"
	mustFailWith(t, dup, "duplicate variant name")

	neg := minimalFlag + "[[flags.demo.variants]]\nname = \"a\"\nweight = -1\n"
	mustFailWith(t, neg, "non-negative")

	zero := minimalFlag + "[[flags.demo.variants]]\nname = \"a\"\nweight = 0\n"
	mustFailWith(t, zero, "total weight")
}

func TestParseReportsAllProblemsAtOnce(t *testing.T) {
	src := "typo = 1\n[flags.a]\nrollout = 500\n[flags.b]\nenabled = \"nope\"\n"
	_, err := Parse([]byte(src))
	ve, ok := err.(*ValidationError)
	if !ok {
		t.Fatalf("want *ValidationError, got %T (%v)", err, err)
	}
	if len(ve.Problems) < 4 {
		t.Fatalf("want at least 4 problems reported together, got %d: %v",
			len(ve.Problems), ve.Problems)
	}
}

func TestParseConfigTreePassesThrough(t *testing.T) {
	f := mustParse(t, "[config.api]\ntimeout_ms = 500\nhosts = [\"a\", \"b\"]\n")
	api := f.Config["api"].(map[string]any)
	if api["timeout_ms"] != int64(500) {
		t.Fatalf("config lost: %v", f.Config)
	}
}

func TestParseHashIsDeterministicAndContentSensitive(t *testing.T) {
	a1 := mustParse(t, minimalFlag)
	a2 := mustParse(t, minimalFlag)
	b := mustParse(t, minimalFlag+"# comment\n")
	if a1.Hash != a2.Hash {
		t.Fatal("hash must be deterministic")
	}
	if a1.Hash == b.Hash {
		t.Fatal("hash must change when the bytes change")
	}
	if len(a1.Hash) != 64 {
		t.Fatalf("hash should be hex sha256, got %q", a1.Hash)
	}
}

func TestFlagNamesAreSorted(t *testing.T) {
	f := mustParse(t, "[flags.zeta]\nenabled = true\n[flags.alpha]\nenabled = true\n[flags.mid]\nenabled = true\n")
	if !reflect.DeepEqual(f.FlagNames(), []string{"alpha", "mid", "zeta"}) {
		t.Fatalf("names not sorted: %v", f.FlagNames())
	}
}

func TestParseSyntaxErrorSurfacesTOMLLineNumber(t *testing.T) {
	_, err := Parse([]byte("[flags.demo]\nenabled = tru\n"))
	if err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("want a line-2 syntax error, got %v", err)
	}
}

func TestOpNamesCoversAllThirteenOperators(t *testing.T) {
	ops := OpNames()
	if len(ops) != 13 {
		t.Fatalf("want 13 ops, got %d: %v", len(ops), ops)
	}
	for i := 1; i < len(ops); i++ {
		if ops[i-1] > ops[i] {
			t.Fatalf("ops not sorted: %v", ops)
		}
	}
}
