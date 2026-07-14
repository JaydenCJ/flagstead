// Package cli implements the flagstead command line. Run is a pure
// function of (argv, stdout, stderr) returning an exit code, so the whole
// surface is testable in-process without spawning binaries.
//
// Exit codes: 0 success, 1 validation failure or unknown flag,
// 2 usage error, 3 runtime error (unreadable file, bind failure).
package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/JaydenCJ/flagstead/internal/eval"
	"github.com/JaydenCJ/flagstead/internal/flagfile"
	"github.com/JaydenCJ/flagstead/internal/version"
)

const usageText = `flagstead — feature flags and remote config from one TOML file

Usage:
  flagstead <command> [flags] [args]

Commands:
  init [path]     write a commented starter flags.toml (default: ./flags.toml)
  check           validate the flag file; exit 1 with every problem listed
  list            list all flags (--format text|json)
  get <flag>      print one flag definition as JSON
  eval <flag>     evaluate a flag for a key (+ --attr k=v ...)
  serve           serve the HTTP API (default 127.0.0.1:4949)
  version         print the version

Common flags (check, list, get, eval, serve):
  --file PATH     flag file to use (default "flags.toml")

Run 'flagstead <command> -h' for command flags.
`

// Run executes argv and returns the process exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usageText)
		return 2
	}
	switch args[0] {
	case "version", "--version", "-v":
		fmt.Fprintf(stdout, "flagstead %s\n", version.Version)
		return 0
	case "help", "--help", "-h":
		fmt.Fprint(stdout, usageText)
		return 0
	case "init":
		return cmdInit(args[1:], stdout, stderr)
	case "check":
		return cmdCheck(args[1:], stdout, stderr)
	case "list":
		return cmdList(args[1:], stdout, stderr)
	case "get":
		return cmdGet(args[1:], stdout, stderr)
	case "eval":
		return cmdEval(args[1:], stdout, stderr)
	case "serve":
		return cmdServe(args[1:], stdout, stderr)
	}
	fmt.Fprintf(stderr, "flagstead: unknown command %q (run 'flagstead help')\n", args[0])
	return 2
}

// newFlagSet builds a FlagSet that reports usage errors on stderr and
// never calls os.Exit.
func newFlagSet(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	return fs
}

// parseInterspersed parses fs over args while allowing positionals and
// flags in any order (`flagstead eval NAME --key u` and `flagstead eval
// --key u NAME` both work; the stdlib flag package alone would stop at
// the first positional). Returns the positional arguments in order.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return pos, nil
		}
		pos = append(pos, rest[0])
		args = rest[1:]
	}
}

// loadFile reads and validates the flag file, mapping failures onto the
// documented exit codes. On error it has already printed to stderr.
func loadFile(path string, stderr io.Writer) (*flagfile.File, int) {
	raw, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(stderr, "flagstead: %v\n", err)
		return nil, 3
	}
	f, err := flagfile.Parse(raw)
	if err != nil {
		for _, line := range strings.Split(err.Error(), "\n") {
			fmt.Fprintf(stderr, "%s: %s\n", path, line)
		}
		return nil, 1
	}
	return f, 0
}

// attrFlags collects repeated --attr key=value pairs.
type attrFlags struct {
	m map[string]string
}

func (a *attrFlags) String() string { return "" }

func (a *attrFlags) Set(s string) error {
	k, v, ok := strings.Cut(s, "=")
	if !ok || k == "" {
		return fmt.Errorf("expected key=value, got %q", s)
	}
	if a.m == nil {
		a.m = map[string]string{}
	}
	a.m[k] = v
	return nil
}

func cmdCheck(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("check", stderr)
	file := fs.String("file", "flags.toml", "flag file to validate")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return 2
	}
	if len(pos) != 0 {
		fmt.Fprintln(stderr, "flagstead: check takes no positional arguments")
		return 2
	}
	f, code := loadFile(*file, stderr)
	if code != 0 {
		return code
	}
	fmt.Fprintf(stdout, "%s: OK (%s, %s)\n",
		*file, plural(len(f.Flags), "flag"), plural(len(f.Config), "config key"))
	return 0
}

// plural renders "1 flag" / "3 flags" — nobody trusts a validator that
// cannot count.
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

func cmdList(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("list", stderr)
	file := fs.String("file", "flags.toml", "flag file to read")
	format := fs.String("format", "text", "output format: text or json")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return 2
	}
	if len(pos) != 0 {
		fmt.Fprintln(stderr, "flagstead: list takes no positional arguments")
		return 2
	}
	f, code := loadFile(*file, stderr)
	if code != 0 {
		return code
	}
	switch *format {
	case "json":
		flags := make([]*flagfile.Flag, 0, len(f.Flags))
		for _, name := range f.FlagNames() {
			flags = append(flags, f.Flags[name])
		}
		printJSON(stdout, flags)
	case "text":
		tw := tabwriter.NewWriter(stdout, 2, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tENABLED\tROLLOUT\tRULES\tDESCRIPTION")
		for _, name := range f.FlagNames() {
			fl := f.Flags[name]
			fmt.Fprintf(tw, "%s\t%v\t%s\t%d\t%s\n",
				fl.Name, fl.Enabled, formatPercent(fl.Rollout), len(fl.Rules), fl.Description)
		}
		tw.Flush()
	default:
		fmt.Fprintf(stderr, "flagstead: unknown --format %q (want text or json)\n", *format)
		return 2
	}
	return 0
}

func cmdGet(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("get", stderr)
	file := fs.String("file", "flags.toml", "flag file to read")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return 2
	}
	if len(pos) != 1 {
		fmt.Fprintln(stderr, "flagstead: get takes exactly one flag name")
		return 2
	}
	f, code := loadFile(*file, stderr)
	if code != 0 {
		return code
	}
	name := pos[0]
	fl, ok := f.Flags[name]
	if !ok {
		fmt.Fprintf(stderr, "flagstead: unknown flag %q in %s (have: %s)\n",
			name, *file, strings.Join(f.FlagNames(), ", "))
		return 1
	}
	printJSON(stdout, fl)
	return 0
}

func cmdEval(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("eval", stderr)
	file := fs.String("file", "flags.toml", "flag file to read")
	key := fs.String("key", "", "evaluation key (user id, session id, ...)")
	format := fs.String("format", "text", "output format: text or json")
	var attrs attrFlags
	fs.Var(&attrs, "attr", "context attribute key=value (repeatable)")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return 2
	}
	if len(pos) != 1 {
		fmt.Fprintln(stderr, "flagstead: eval takes exactly one flag name")
		return 2
	}
	f, code := loadFile(*file, stderr)
	if code != 0 {
		return code
	}
	name := pos[0]
	fl, ok := f.Flags[name]
	if !ok {
		fmt.Fprintf(stderr, "flagstead: unknown flag %q in %s (have: %s)\n",
			name, *file, strings.Join(f.FlagNames(), ", "))
		return 1
	}
	res := eval.Evaluate(fl, eval.Context{Key: *key, Attributes: attrs.m})
	switch *format {
	case "json":
		printJSON(stdout, res)
	case "text":
		printResult(stdout, res)
	default:
		fmt.Fprintf(stderr, "flagstead: unknown --format %q (want text or json)\n", *format)
		return 2
	}
	return 0
}

func printResult(w io.Writer, res eval.Result) {
	fmt.Fprintf(w, "flag     %s\n", res.Flag)
	fmt.Fprintf(w, "key      %s\n", res.Key)
	fmt.Fprintf(w, "enabled  %v\n", res.Enabled)
	if res.Variant != "" {
		fmt.Fprintf(w, "variant  %s\n", res.Variant)
	}
	fmt.Fprintf(w, "reason   %s\n", res.Reason)
	if res.RuleIndex >= 0 {
		fmt.Fprintf(w, "rule     %d\n", res.RuleIndex)
	}
	if res.Bucket >= 0 {
		fmt.Fprintf(w, "bucket   %d\n", res.Bucket)
	}
}

func printJSON(w io.Writer, v any) {
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		// Everything we marshal is plain data; this cannot realistically fail.
		fmt.Fprintf(w, "%v\n", v)
		return
	}
	fmt.Fprintln(w, string(out))
}

func formatPercent(p float64) string {
	s := fmt.Sprintf("%.2f", p)
	s = strings.TrimRight(s, "0")
	s = strings.TrimRight(s, ".")
	return s + "%"
}
