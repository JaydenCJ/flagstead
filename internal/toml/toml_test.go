// Tests for the TOML subset parser. Constructs are grouped by feature;
// the rejection table asserts on messages and line numbers, because
// "flagstead check points at the exact line" is a headline claim.
package toml

import (
	"reflect"
	"strings"
	"testing"
)

func mustParse(t *testing.T, src string) map[string]any {
	t.Helper()
	doc, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse(%q) failed: %v", src, err)
	}
	return doc
}

func mustFail(t *testing.T, src string) *ParseError {
	t.Helper()
	_, err := Parse([]byte(src))
	if err == nil {
		t.Fatalf("Parse(%q) unexpectedly succeeded", src)
	}
	pe, ok := err.(*ParseError)
	if !ok {
		t.Fatalf("Parse(%q) returned %T, want *ParseError", src, err)
	}
	return pe
}

func TestParseEmptyAndCommentOnlyInput(t *testing.T) {
	for _, src := range []string{"", "# a comment\n\n   # another\n"} {
		if doc := mustParse(t, src); len(doc) != 0 {
			t.Fatalf("Parse(%q): expected empty document, got %v", src, doc)
		}
	}
}

func TestParseBasicScalars(t *testing.T) {
	doc := mustParse(t, "s = \"hi\"\nn = 42\nf = 2.5\nb = true\nneg = -7\n")
	want := map[string]any{"s": "hi", "n": int64(42), "f": 2.5, "b": true, "neg": int64(-7)}
	if !reflect.DeepEqual(doc, want) {
		t.Fatalf("got %v, want %v", doc, want)
	}
}

func TestParseNumberFormats(t *testing.T) {
	doc := mustParse(t,
		"a = 1_000_000\nb = 0xff\nc = 0o755\nd = 0b1010\ne = +5\n"+
			"f = 3.14\ng = 1e3\nh = -0.5\ni = 6.02e23\n")
	want := map[string]any{
		"a": int64(1000000), "b": int64(255), "c": int64(493),
		"d": int64(10), "e": int64(5),
		"f": 3.14, "g": 1000.0, "h": -0.5, "i": 6.02e23,
	}
	if !reflect.DeepEqual(doc, want) {
		t.Fatalf("got %v, want %v", doc, want)
	}
}

func TestParseStringEscapes(t *testing.T) {
	doc := mustParse(t, `s = "tab\there\nnl \"q\" back\\slash"`+"\nu = \"é\\U0001F600\"\n")
	if want := "tab\there\nnl \"q\" back\\slash"; doc["s"] != want {
		t.Fatalf("got %q, want %q", doc["s"], want)
	}
	if doc["u"] != "é😀" {
		t.Fatalf("unicode escape: got %q, want é😀", doc["u"])
	}
}

func TestParseLiteralStringKeepsBackslashes(t *testing.T) {
	doc := mustParse(t, `p = 'C:\net\tmp'`+"\n")
	if doc["p"] != `C:\net\tmp` {
		t.Fatalf("got %q", doc["p"])
	}
}

// TestParseRejectsMalformedInput covers every rejection path; wantMsg (when
// set) pins the message users see, wantLine (when >0) pins the position.
func TestParseRejectsMalformedInput(t *testing.T) {
	cases := []struct {
		name     string
		src      string
		wantMsg  string
		wantLine int
	}{
		{"leading zero int", "port = 0123\n", "invalid value", 1},
		{"invalid escape", "s = \"bad \\x escape\"\n", "escape", 1},
		{"unterminated string", "s = \"never closed\n", "", 1},
		{"multiline string", "s = \"\"\"multi\"\"\"\n", "multi-line strings are not supported", 1},
		{"datetime", "when = 2026-01-02\n", "date/time values are not supported", 1},
		{"missing equals", "[t]\nkey \"value\"\n", "expected '='", 2},
		{"trailing garbage", "a = 1 2\n", "after value", 1},
		{"unterminated array", "xs = [1, 2\n", "unterminated array", 0},
		{"duplicate table", "[a]\nx = 1\n[a]\ny = 2\n", "already defined", 3},
		{"value redefined as table", "a = 1\n[a]\nx = 2\n", "cannot redefine", 2},
		{"newline in inline table", "t = { a = 1,\n b = 2 }\n", "inline table", 1},
		{"bare word value", "a = yes\n", "invalid value", 1},
	}
	for _, tc := range cases {
		pe := mustFail(t, tc.src)
		if tc.wantMsg != "" && !strings.Contains(pe.Msg, tc.wantMsg) {
			t.Errorf("%s: message %q should contain %q", tc.name, pe.Msg, tc.wantMsg)
		}
		if tc.wantLine > 0 && pe.Line != tc.wantLine {
			t.Errorf("%s: error line = %d, want %d", tc.name, pe.Line, tc.wantLine)
		}
	}
}

func TestParseArrays(t *testing.T) {
	src := "tags = [\"a\", \"b\", \"c\"]\n" +
		"xs = [\n  1, # one\n  2,\n  3, # trailing comma is legal\n]\n" +
		"empty = []\n"
	doc := mustParse(t, src)
	if !reflect.DeepEqual(doc["tags"], []any{"a", "b", "c"}) {
		t.Fatalf("tags: got %v", doc["tags"])
	}
	if !reflect.DeepEqual(doc["xs"], []any{int64(1), int64(2), int64(3)}) {
		t.Fatalf("multiline array: got %v", doc["xs"])
	}
	if !reflect.DeepEqual(doc["empty"], []any{}) {
		t.Fatalf("empty array: got %v", doc["empty"])
	}
}

func TestParseInlineTable(t *testing.T) {
	doc := mustParse(t, `point = { x = 1, y = "two" }`+"\n")
	want := map[string]any{"x": int64(1), "y": "two"}
	if !reflect.DeepEqual(doc["point"], want) {
		t.Fatalf("got %v, want %v", doc["point"], want)
	}
}

func TestParseDottedAndQuotedKeys(t *testing.T) {
	doc := mustParse(t, "a.b.c = 1\na.b.d = 2\n[flags]\n\"new.checkout v2\" = true\n")
	ab := doc["a"].(map[string]any)["b"].(map[string]any)
	if ab["c"] != int64(1) || ab["d"] != int64(2) {
		t.Fatalf("dotted keys: got %v", doc)
	}
	if doc["flags"].(map[string]any)["new.checkout v2"] != true {
		t.Fatalf("quoted key lost: %v", doc)
	}
}

func TestParseTableHeadersNest(t *testing.T) {
	doc := mustParse(t, "[flags.checkout]\nenabled = true\n[config]\nx = 1\n")
	flags := doc["flags"].(map[string]any)
	checkout := flags["checkout"].(map[string]any)
	if checkout["enabled"] != true || doc["config"].(map[string]any)["x"] != int64(1) {
		t.Fatalf("unexpected document: %v", doc)
	}
}

func TestParseArrayOfTablesPreservesOrder(t *testing.T) {
	src := "[[rules]]\nop = \"eq\"\n[[rules]]\nop = \"in\"\n[[rules]]\nop = \"ne\"\n"
	doc := mustParse(t, src)
	rules := doc["rules"].([]any)
	if len(rules) != 3 {
		t.Fatalf("want 3 rule tables, got %d", len(rules))
	}
	ops := []string{}
	for _, r := range rules {
		ops = append(ops, r.(map[string]any)["op"].(string))
	}
	if !reflect.DeepEqual(ops, []string{"eq", "in", "ne"}) {
		t.Fatalf("order lost: %v", ops)
	}
}

func TestParseSubtableUnderArrayOfTablesElement(t *testing.T) {
	// [servers.limits] after [[servers]] must attach to the LAST element.
	src := "[[servers]]\nname = \"a\"\n[servers.limits]\ncpu = 2\n"
	doc := mustParse(t, src)
	servers := doc["servers"].([]any)
	limits := servers[0].(map[string]any)["limits"].(map[string]any)
	if limits["cpu"] != int64(2) {
		t.Fatalf("subtable not attached: %v", doc)
	}
}

func TestParseDuplicateKeyRejectedWithLine(t *testing.T) {
	pe := mustFail(t, "a = 1\na = 2\n")
	if pe.Line != 2 || !strings.Contains(pe.Msg, "duplicate key") {
		t.Fatalf("got line %d msg %q", pe.Line, pe.Msg)
	}
}

func TestParseErrorLineNumbersCountCommentsAndBlanks(t *testing.T) {
	src := "# header\n\n[flags]\n# comment\nok = true\nbroken =\n"
	pe := mustFail(t, src)
	if pe.Line != 6 {
		t.Fatalf("error line = %d, want 6", pe.Line)
	}
}

func TestParseCRLFAndTrailingComments(t *testing.T) {
	doc := mustParse(t, "a = 1\r\nb = 2 # trailing comment\r\n")
	if doc["a"] != int64(1) || doc["b"] != int64(2) {
		t.Fatalf("CRLF/comment input mishandled: %v", doc)
	}
}
