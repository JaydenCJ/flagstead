// Package version pins the single source of truth for the flagstead
// version string. go.mod, CHANGELOG.md and the README badges must agree
// with this constant; scripts/smoke.sh asserts on it.
package version

// Version is the current flagstead release.
const Version = "0.1.0"
