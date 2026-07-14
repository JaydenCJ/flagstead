// init: write a commented starter file that demonstrates every construct
// flagstead understands. The template must always pass `flagstead check`;
// a test enforces that.
package cli

import (
	"fmt"
	"io"
	"os"
)

// starterFile is the template written by `flagstead init`.
const starterFile = `# flags.toml — flagstead flag file
# Validate with:  flagstead check --file flags.toml
# Serve with:     flagstead serve --file flags.toml

version = 1

# A plain boolean kill switch.
[flags.dark-mode]
description = "Dark theme for the dashboard"
enabled = false

# A sticky percent rollout: 25% of keys, decided by a stable hash of the
# key — raising the percentage never kicks out an already-enabled key.
[flags.new-checkout]
description = "New checkout flow"
enabled = true
rollout = 25
tags = ["checkout", "q3"]

# Targeting rule: these countries skip the percent gate entirely.
[[flags.new-checkout.rules]]
attribute = "country"
op = "in"
values = ["JP", "DE"]
rollout = 100

# Weighted variants for an A/B test (picked deterministically per key).
[flags.cta-copy]
description = "Call-to-action wording experiment"
enabled = true

[[flags.cta-copy.variants]]
name = "control"
weight = 50

[[flags.cta-copy.variants]]
name = "action"
weight = 50

# Remote config: arbitrary values served at /v1/config.
[config.api]
timeout_ms = 500
retries = 3

[config.ui]
banner = "Welcome!"
`

func cmdInit(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("init", stderr)
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return 2
	}
	path := "flags.toml"
	switch len(pos) {
	case 0:
	case 1:
		path = pos[0]
	default:
		fmt.Fprintln(stderr, "flagstead: init takes at most one path")
		return 2
	}
	if _, err := os.Stat(path); err == nil {
		fmt.Fprintf(stderr, "flagstead: refusing to overwrite existing %s\n", path)
		return 1
	}
	if err := os.WriteFile(path, []byte(starterFile), 0o644); err != nil {
		fmt.Fprintf(stderr, "flagstead: %v\n", err)
		return 3
	}
	fmt.Fprintf(stdout, "wrote %s (3 flags, 2 config sections) — try:\n", path)
	fmt.Fprintf(stdout, "  flagstead eval new-checkout --file %s --key user-42\n", path)
	fmt.Fprintf(stdout, "  flagstead serve --file %s\n", path)
	return 0
}
