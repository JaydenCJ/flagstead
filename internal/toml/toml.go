// Package toml implements the deliberately small TOML subset that
// flagstead flag files use: tables, arrays of tables, dotted keys, basic
// and literal strings, integers (decimal/hex/octal/binary, underscores),
// floats, booleans, arrays, and inline tables. Date/time values and
// multi-line strings are rejected with a targeted error instead of being
// half-supported. Every error carries a 1-based line number so `flagstead
// check` can point at the exact spot in the file.
//
// The parser is hand-written recursive descent over the raw bytes; it has
// no dependencies and allocates only the resulting document tree.
package toml

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// ParseError is the error type returned for any malformed input.
type ParseError struct {
	Line int    // 1-based line the error was detected on
	Msg  string // human-readable description
}

func (e *ParseError) Error() string { return fmt.Sprintf("line %d: %s", e.Line, e.Msg) }

// Parse decodes src into a tree of map[string]any / []any / scalar values.
// Scalars are string, int64, float64 and bool.
func Parse(src []byte) (map[string]any, error) {
	p := &parser{
		src:      string(src),
		line:     1,
		root:     map[string]any{},
		explicit: map[string]bool{},
		aot:      map[string]bool{},
	}
	p.cur = p.root
	if err := p.run(); err != nil {
		return nil, err
	}
	return p.root, nil
}

type parser struct {
	src  string
	pos  int
	line int

	root map[string]any
	cur  map[string]any // table that plain key/value lines land in

	explicit map[string]bool // paths defined via a [table] header
	aot      map[string]bool // paths created via an [[array-of-tables]] header
}

func (p *parser) run() error {
	for {
		p.skipBlank()
		if p.eof() {
			return nil
		}
		if p.peek() == '[' {
			if err := p.header(); err != nil {
				return err
			}
		} else {
			if err := p.keyval(p.cur); err != nil {
				return err
			}
		}
		if err := p.lineEnd(); err != nil {
			return err
		}
	}
}

// --- lexical helpers -------------------------------------------------------

func (p *parser) eof() bool  { return p.pos >= len(p.src) }
func (p *parser) peek() byte { return p.src[p.pos] }

func (p *parser) advance() {
	if p.src[p.pos] == '\n' {
		p.line++
	}
	p.pos++
}

// skipSpace consumes spaces and tabs only (never newlines).
func (p *parser) skipSpace() {
	for !p.eof() && (p.peek() == ' ' || p.peek() == '\t') {
		p.pos++
	}
}

// skipBlank consumes whitespace, newlines and full-line comments.
func (p *parser) skipBlank() {
	for !p.eof() {
		switch p.peek() {
		case ' ', '\t', '\r', '\n':
			p.advance()
		case '#':
			for !p.eof() && p.peek() != '\n' {
				p.pos++
			}
		default:
			return
		}
	}
}

// lineEnd asserts that nothing but spaces or a comment follows on the line.
func (p *parser) lineEnd() error {
	p.skipSpace()
	if !p.eof() && p.peek() == '#' {
		for !p.eof() && p.peek() != '\n' {
			p.pos++
		}
	}
	if p.eof() {
		return nil
	}
	switch p.peek() {
	case '\n':
		p.advance()
		return nil
	case '\r':
		p.pos++
		if !p.eof() && p.peek() == '\n' {
			p.advance()
			return nil
		}
		return p.errf("bare carriage return")
	}
	return p.errf("unexpected characters after value")
}

func (p *parser) errf(format string, args ...any) error {
	return &ParseError{Line: p.line, Msg: fmt.Sprintf(format, args...)}
}

// --- keys ------------------------------------------------------------------

func isBareKeyChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' ||
		c >= '0' && c <= '9' || c == '_' || c == '-'
}

// keyPath parses a possibly dotted key such as `flags."new checkout".salt`.
func (p *parser) keyPath() ([]string, error) {
	var keys []string
	for {
		p.skipSpace()
		k, err := p.keySegment()
		if err != nil {
			return nil, err
		}
		keys = append(keys, k)
		p.skipSpace()
		if !p.eof() && p.peek() == '.' {
			p.pos++
			continue
		}
		return keys, nil
	}
}

func (p *parser) keySegment() (string, error) {
	if p.eof() {
		return "", p.errf("expected a key")
	}
	switch p.peek() {
	case '"':
		return p.basicString()
	case '\'':
		return p.literalString()
	}
	start := p.pos
	for !p.eof() && isBareKeyChar(p.peek()) {
		p.pos++
	}
	if p.pos == start {
		return "", p.errf("expected a key, found %q", string(p.peek()))
	}
	return p.src[start:p.pos], nil
}

func pathKey(segs []string) string { return strings.Join(segs, "\x00") }

func dotted(segs []string) string { return strings.Join(segs, ".") }

// --- table headers ---------------------------------------------------------

func (p *parser) header() error {
	p.pos++ // consume '['
	isAoT := false
	if !p.eof() && p.peek() == '[' {
		isAoT = true
		p.pos++
	}
	p.skipSpace()
	keys, err := p.keyPath()
	if err != nil {
		return err
	}
	p.skipSpace()
	if p.eof() || p.peek() != ']' {
		return p.errf("expected ']' to close table header")
	}
	p.pos++
	if isAoT {
		if p.eof() || p.peek() != ']' {
			return p.errf("expected ']]' to close array-of-tables header")
		}
		p.pos++
	}

	parent, err := p.descend(keys[:len(keys)-1])
	if err != nil {
		return err
	}
	last := keys[len(keys)-1]
	pk := pathKey(keys)

	if isAoT {
		elem := map[string]any{}
		existing, ok := parent[last]
		if !ok {
			parent[last] = []any{elem}
		} else {
			arr, isArr := existing.([]any)
			if !isArr || !p.aot[pk] {
				return p.errf("cannot append to %q: it is not an array of tables", dotted(keys))
			}
			parent[last] = append(arr, elem)
		}
		p.aot[pk] = true
		p.cur = elem
		return nil
	}

	existing, ok := parent[last]
	if !ok {
		m := map[string]any{}
		parent[last] = m
		p.cur = m
	} else {
		m, isMap := existing.(map[string]any)
		if !isMap {
			return p.errf("cannot redefine %q as a table", dotted(keys))
		}
		if p.explicit[pk] {
			return p.errf("table %q is already defined", dotted(keys))
		}
		p.cur = m
	}
	p.explicit[pk] = true
	return nil
}

// descend walks (creating implicit tables) to the table at segs; when an
// intermediate segment is an array of tables, its last element is entered,
// matching TOML's super-table rules.
func (p *parser) descend(segs []string) (map[string]any, error) {
	t := p.root
	for i, s := range segs {
		v, ok := t[s]
		if !ok {
			m := map[string]any{}
			t[s] = m
			t = m
			continue
		}
		switch x := v.(type) {
		case map[string]any:
			t = x
		case []any:
			if !p.aot[pathKey(segs[:i+1])] {
				return nil, p.errf("key %q is a plain array, not a table", dotted(segs[:i+1]))
			}
			t = x[len(x)-1].(map[string]any)
		default:
			return nil, p.errf("key %q is already a value, not a table", dotted(segs[:i+1]))
		}
	}
	return t, nil
}

// --- key/value pairs -------------------------------------------------------

func (p *parser) keyval(table map[string]any) error {
	keys, err := p.keyPath()
	if err != nil {
		return err
	}
	p.skipSpace()
	if p.eof() || p.peek() != '=' {
		return p.errf("expected '=' after key %q", dotted(keys))
	}
	p.pos++
	p.skipSpace()
	val, err := p.value()
	if err != nil {
		return err
	}

	t := table
	for i, k := range keys[:len(keys)-1] {
		v, ok := t[k]
		if !ok {
			m := map[string]any{}
			t[k] = m
			t = m
			continue
		}
		m, isMap := v.(map[string]any)
		if !isMap {
			return p.errf("key %q is already a value, not a table", dotted(keys[:i+1]))
		}
		t = m
	}
	last := keys[len(keys)-1]
	if _, exists := t[last]; exists {
		return p.errf("duplicate key %q", dotted(keys))
	}
	t[last] = val
	return nil
}

// --- values ------------------------------------------------------------------

func (p *parser) value() (any, error) {
	if p.eof() {
		return nil, p.errf("expected a value")
	}
	switch p.peek() {
	case '"':
		if strings.HasPrefix(p.src[p.pos:], `"""`) {
			return nil, p.errf("multi-line strings are not supported by flagstead's TOML subset")
		}
		return p.basicString()
	case '\'':
		if strings.HasPrefix(p.src[p.pos:], "'''") {
			return nil, p.errf("multi-line strings are not supported by flagstead's TOML subset")
		}
		return p.literalString()
	case '[':
		return p.array()
	case '{':
		return p.inlineTable()
	}
	return p.scalarToken()
}

func (p *parser) basicString() (string, error) {
	p.pos++ // opening quote
	var b strings.Builder
	for {
		if p.eof() {
			return "", p.errf("unterminated string")
		}
		c := p.peek()
		if c == '\n' {
			return "", p.errf("newline inside single-line string (multi-line strings are not supported)")
		}
		p.pos++
		switch c {
		case '"':
			return b.String(), nil
		case '\\':
			if err := p.escape(&b); err != nil {
				return "", err
			}
		default:
			b.WriteByte(c)
		}
	}
}

func (p *parser) escape(b *strings.Builder) error {
	if p.eof() {
		return p.errf("unterminated escape sequence")
	}
	e := p.peek()
	p.pos++
	switch e {
	case 'n':
		b.WriteByte('\n')
	case 't':
		b.WriteByte('\t')
	case 'r':
		b.WriteByte('\r')
	case 'b':
		b.WriteByte('\b')
	case 'f':
		b.WriteByte('\f')
	case '"':
		b.WriteByte('"')
	case '\\':
		b.WriteByte('\\')
	case 'u', 'U':
		n := 4
		if e == 'U' {
			n = 8
		}
		if p.pos+n > len(p.src) {
			return p.errf("truncated unicode escape")
		}
		hex := p.src[p.pos : p.pos+n]
		v, err := strconv.ParseUint(hex, 16, 32)
		if err != nil {
			return p.errf("invalid unicode escape \\%c%s", e, hex)
		}
		if !utf8.ValidRune(rune(v)) {
			return p.errf("escape \\%c%s is not a valid unicode scalar value", e, hex)
		}
		b.WriteRune(rune(v))
		p.pos += n
	default:
		return p.errf("invalid escape sequence \\%c", e)
	}
	return nil
}

func (p *parser) literalString() (string, error) {
	p.pos++ // opening quote
	start := p.pos
	for {
		if p.eof() {
			return "", p.errf("unterminated string")
		}
		c := p.peek()
		if c == '\n' {
			return "", p.errf("newline inside single-line string (multi-line strings are not supported)")
		}
		p.pos++
		if c == '\'' {
			return p.src[start : p.pos-1], nil
		}
	}
}

func (p *parser) array() (any, error) {
	p.pos++ // '['
	out := []any{}
	for {
		p.skipBlank() // newlines and comments are legal inside arrays
		if p.eof() {
			return nil, p.errf("unterminated array")
		}
		if p.peek() == ']' {
			p.pos++
			return out, nil
		}
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		out = append(out, v)
		p.skipBlank()
		if p.eof() {
			return nil, p.errf("unterminated array")
		}
		switch p.peek() {
		case ',':
			p.pos++
		case ']':
			p.pos++
			return out, nil
		default:
			return nil, p.errf("expected ',' or ']' in array")
		}
	}
}

func (p *parser) inlineTable() (any, error) {
	p.pos++ // '{'
	m := map[string]any{}
	p.skipSpace()
	if !p.eof() && p.peek() == '}' {
		p.pos++
		return m, nil
	}
	for {
		p.skipSpace()
		if !p.eof() && p.peek() == '\n' {
			return nil, p.errf("newlines are not allowed inside inline tables")
		}
		if err := p.keyval(m); err != nil {
			return nil, err
		}
		p.skipSpace()
		if p.eof() {
			return nil, p.errf("unterminated inline table")
		}
		switch p.peek() {
		case ',':
			p.pos++
		case '}':
			p.pos++
			return m, nil
		case '\n':
			return nil, p.errf("newlines are not allowed inside inline tables")
		default:
			return nil, p.errf("expected ',' or '}' in inline table")
		}
	}
}

func isValueDelim(c byte) bool {
	switch c {
	case ' ', '\t', '\r', '\n', ',', ']', '}', '#':
		return true
	}
	return false
}

func (p *parser) scalarToken() (any, error) {
	start := p.pos
	for !p.eof() && !isValueDelim(p.peek()) {
		p.pos++
	}
	tok := p.src[start:p.pos]
	if tok == "" {
		return nil, p.errf("expected a value")
	}
	switch tok {
	case "true":
		return true, nil
	case "false":
		return false, nil
	}
	if v, ok := parseInt(tok); ok {
		return v, nil
	}
	if v, ok := parseFloat(tok); ok {
		return v, nil
	}
	if looksLikeDateTime(tok) {
		return nil, p.errf("date/time values are not supported by flagstead's TOML subset: %q", tok)
	}
	return nil, p.errf("invalid value %q", tok)
}

func looksLikeDateTime(tok string) bool {
	if len(tok) >= 10 && tok[4] == '-' && tok[7] == '-' {
		return true // 2026-01-02 style date
	}
	return strings.Count(tok, ":") == 2 // 07:30:00 style local time
}

func validUnderscores(s string) bool {
	if strings.HasPrefix(s, "_") || strings.HasSuffix(s, "_") {
		return false
	}
	return !strings.Contains(s, "__")
}

func parseInt(tok string) (int64, bool) {
	s := tok
	sign := ""
	if strings.HasPrefix(s, "+") {
		s = s[1:]
	} else if strings.HasPrefix(s, "-") {
		sign = "-"
		s = s[1:]
	}
	if s == "" {
		return 0, false
	}
	if strings.Contains(s, "_") {
		if !validUnderscores(s) {
			return 0, false
		}
		s = strings.ReplaceAll(s, "_", "")
	}
	base := 10
	switch {
	case strings.HasPrefix(s, "0x"):
		base, s = 16, s[2:]
	case strings.HasPrefix(s, "0o"):
		base, s = 8, s[2:]
	case strings.HasPrefix(s, "0b"):
		base, s = 2, s[2:]
	default:
		if len(s) > 1 && s[0] == '0' {
			return 0, false // TOML forbids leading zeros
		}
	}
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseInt(sign+s, base, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

func parseFloat(tok string) (float64, bool) {
	s := tok
	if strings.Contains(s, "_") {
		if !validUnderscores(strings.TrimLeft(s, "+-")) {
			return 0, false
		}
		s = strings.ReplaceAll(s, "_", "")
	}
	// A TOML float must contain a fractional dot or an exponent; anything
	// hex-ish already matched parseInt or is invalid.
	if s == "" || !strings.ContainsAny(s, ".eE") || strings.ContainsAny(s, "xXoO") {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}
