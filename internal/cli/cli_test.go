// In-process CLI tests: Run(argv, stdout, stderr) is exercised exactly as
// main() calls it, asserting on output text and the documented exit codes
// (0 ok, 1 validation/unknown flag, 2 usage, 3 runtime).
package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sampleFile = `version = 1

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
`

// run invokes the CLI and returns (exit code, stdout, stderr).
func run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := Run(args, &out, &errb)
	return code, out.String(), errb.String()
}

// withSample writes the sample flag file into a temp dir and returns its path.
func withSample(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "flags.toml")
	if err := os.WriteFile(path, []byte(sampleFile), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestVersionAndHelpExitZero(t *testing.T) {
	for _, arg := range []string{"version", "--version", "-v"} {
		code, out, _ := run(t, arg)
		if code != 0 || out != "flagstead 0.1.0\n" {
			t.Fatalf("%s: code=%d out=%q", arg, code, out)
		}
	}
	code, out, _ := run(t, "help")
	if code != 0 || !strings.Contains(out, "flagstead <command>") {
		t.Fatalf("help: code=%d out=%q", code, out)
	}
}

func TestUsageErrorsExit2(t *testing.T) {
	code, _, errb := run(t)
	if code != 2 || !strings.Contains(errb, "Usage:") {
		t.Fatalf("no args: code=%d stderr=%q", code, errb)
	}
	code, _, errb = run(t, "frobnicate")
	if code != 2 || !strings.Contains(errb, "unknown command") {
		t.Fatalf("unknown command: code=%d stderr=%q", code, errb)
	}
}

func TestCheckValidFile(t *testing.T) {
	path := withSample(t)
	code, out, _ := run(t, "check", "--file", path)
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	// Singular/plural must both be right — "1 config keys" is the kind of
	// papercut that erodes trust in a validator.
	if !strings.Contains(out, "OK (2 flags, 1 config key)") {
		t.Fatalf("out=%q", out)
	}
}

func TestCheckInvalidFileListsEveryProblemWithPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.toml")
	content := "[flags.a]\nrollout = 900\n[flags.b]\nenabled = 3\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, errb := run(t, "check", "--file", path)
	if code != 1 {
		t.Fatalf("code=%d, want 1", code)
	}
	// One prefixed line per problem: a bad rollout, a bad enabled type,
	// and two missing/broken enabled keys.
	if strings.Count(errb, path+": ") < 3 {
		t.Fatalf("expected every problem prefixed with the path:\n%s", errb)
	}

	// A pure syntax error points at the exact line instead.
	if err := os.WriteFile(path, []byte("[flags.a]\nenabled = tru\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, errb = run(t, "check", "--file", path)
	if code != 1 || !strings.Contains(errb, "line 2") {
		t.Fatalf("syntax error: code=%d stderr=%q", code, errb)
	}
}

func TestMissingFileIsARuntimeError(t *testing.T) {
	code, _, errb := run(t, "check", "--file", filepath.Join(t.TempDir(), "nope.toml"))
	if code != 3 || errb == "" {
		t.Fatalf("code=%d stderr=%q", code, errb)
	}
}

func TestListTextIsSortedWithHeader(t *testing.T) {
	path := withSample(t)
	code, out, _ := run(t, "list", "--file", path)
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "NAME") {
		t.Fatalf("out=%q", out)
	}
	if !strings.Contains(lines[1], "dark-mode") || !strings.Contains(lines[2], "new-checkout") {
		t.Fatalf("rows not sorted: %q", out)
	}
	if !strings.Contains(lines[2], "25%") {
		t.Fatalf("rollout not rendered: %q", lines[2])
	}
	// Unknown formats are usage errors, not silent fallbacks.
	code, _, errb := run(t, "list", "--file", path, "--format", "yaml")
	if code != 2 || !strings.Contains(errb, "yaml") {
		t.Fatalf("bad format: code=%d stderr=%q", code, errb)
	}
}

func TestListJSONIsParseable(t *testing.T) {
	path := withSample(t)
	code, out, _ := run(t, "list", "--file", path, "--format", "json")
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	var flags []map[string]any
	if err := json.Unmarshal([]byte(out), &flags); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if len(flags) != 2 || flags[0]["name"] != "dark-mode" {
		t.Fatalf("unexpected list: %v", flags)
	}
}

func TestGetPrintsFlagDefinition(t *testing.T) {
	path := withSample(t)
	code, out, _ := run(t, "get", "new-checkout", "--file", path)
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	var fl map[string]any
	if err := json.Unmarshal([]byte(out), &fl); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if fl["name"] != "new-checkout" || fl["rollout"] != float64(25) {
		t.Fatalf("unexpected flag: %v", fl)
	}
	// An unknown name exits 1 and lists what IS available.
	code, _, errb := run(t, "get", "ghost", "--file", path)
	if code != 1 || !strings.Contains(errb, "have: dark-mode, new-checkout") {
		t.Fatalf("unknown flag: code=%d stderr=%q", code, errb)
	}
}

func TestEvalTextOutputMatchedRule(t *testing.T) {
	path := withSample(t)
	code, out, _ := run(t, "eval", "new-checkout", "--file", path,
		"--key", "user-1", "--attr", "country=JP")
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	for _, want := range []string{"flag     new-checkout", "enabled  true", "reason   rule", "rule     0"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

func TestEvalJSONOutputRoundTrips(t *testing.T) {
	path := withSample(t)
	code, out, _ := run(t, "eval", "dark-mode", "--file", path,
		"--key", "user-1", "--format", "json")
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if res["enabled"] != false || res["reason"] != "flag_disabled" {
		t.Fatalf("unexpected result: %v", res)
	}
}

func TestEvalUsageErrors(t *testing.T) {
	path := withSample(t)
	code, _, errb := run(t, "eval", "dark-mode", "--file", path, "--attr", "noequals")
	if code != 2 || !strings.Contains(errb, "key=value") {
		t.Fatalf("bad --attr: code=%d stderr=%q", code, errb)
	}
	if code, _, _ := run(t, "eval", "--file", path); code != 2 {
		t.Fatalf("no name: code=%d, want 2", code)
	}
	if code, _, _ := run(t, "eval", "a", "b", "--file", path); code != 2 {
		t.Fatalf("two names: code=%d, want 2", code)
	}
}

func TestInitWritesStarterThatPassesCheck(t *testing.T) {
	path := filepath.Join(t.TempDir(), "flags.toml")
	code, out, _ := run(t, "init", path)
	if code != 0 || !strings.Contains(out, "wrote "+path) {
		t.Fatalf("code=%d out=%q", code, out)
	}
	// The starter must be valid by flagstead's own strict validation.
	code, out, errb := run(t, "check", "--file", path)
	if code != 0 {
		t.Fatalf("starter file fails check: %s", errb)
	}
	if !strings.Contains(out, "3 flags") {
		t.Fatalf("starter should define 3 flags: %q", out)
	}
}

func TestInitRefusesToOverwrite(t *testing.T) {
	path := withSample(t)
	code, _, errb := run(t, "init", path)
	if code != 1 || !strings.Contains(errb, "refusing to overwrite") {
		t.Fatalf("code=%d stderr=%q", code, errb)
	}
	// The original content is untouched.
	raw, _ := os.ReadFile(path)
	if string(raw) != sampleFile {
		t.Fatal("init clobbered an existing file")
	}
}

func TestInitStarterEvalIsStable(t *testing.T) {
	// The README quickstart shows this exact evaluation; pin it so the
	// docs can never drift from reality.
	path := filepath.Join(t.TempDir(), "flags.toml")
	if code, _, _ := run(t, "init", path); code != 0 {
		t.Fatal("init failed")
	}
	code, out, _ := run(t, "eval", "new-checkout", "--file", path,
		"--key", "user-42", "--format", "json")
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err)
	}
	if res["reason"] != "rollout" {
		t.Fatalf("expected the rollout gate to decide for user-42: %v", res)
	}
}

func TestServeRejectsInvalidFileBeforeBinding(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.toml")
	if err := os.WriteFile(path, []byte("enabled ="), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, errb := run(t, "serve", "--file", path, "--addr", "127.0.0.1:0")
	if code != 1 || !strings.Contains(errb, path+": ") {
		t.Fatalf("code=%d stderr=%q", code, errb)
	}
}

func TestServeArgumentAndFileErrors(t *testing.T) {
	code, _, _ := run(t, "serve", "--file",
		filepath.Join(t.TempDir(), "nope.toml"), "--addr", "127.0.0.1:0")
	if code != 3 {
		t.Fatalf("missing file: code=%d, want 3", code)
	}
	path := withSample(t)
	code, _, errb := run(t, "serve", "extra", "--file", path)
	if code != 2 || !strings.Contains(errb, "no positional") {
		t.Fatalf("positional arg: code=%d stderr=%q", code, errb)
	}
}
