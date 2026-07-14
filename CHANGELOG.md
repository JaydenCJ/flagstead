# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.1.0] - 2026-07-13

### Added

- Single-binary HTTP server (`flagstead serve`) exposing flags, per-key
  evaluation (single and batch), and remote config from one TOML file,
  bound to 127.0.0.1 by default with zero runtime dependencies.
- Strong ETag on every snapshot and evaluation endpoint, derived from the
  file's SHA-256, so clients poll with `If-None-Match` and pay one cheap
  304 per interval until the file actually changes.
- Hot reload on mtime/size change with a last-good-snapshot safety net: a
  broken edit never takes flags down; `/healthz` reports `degraded` with
  the parse error until the file is fixed, then recovers automatically.
- Deterministic sticky percent rollouts: FNV-1a bucketing over 10,000
  buckets (basis-point precision), per-flag salt, raising a percentage
  never kicks out an already-enabled key.
- Targeting rules with 13 operators (eq, ne, in, not_in, contains,
  prefix, suffix, gt, gte, lt, lte, exists, not_exists), first-match-wins
  ordering, per-rule rollout overrides and forced outcomes; missing
  attributes fail closed.
- Weighted variants for A/B tests, picked by an independent hash so the
  variant split is uncorrelated with the rollout population.
- Hand-written zero-dependency TOML-subset parser with 1-based line
  numbers on every error; date/times and multi-line strings are rejected
  with targeted messages.
- Strict file validation (`flagstead check`): unknown keys, missing
  `enabled`, out-of-range percentages and bad rule shapes are all
  reported at once, prefixed with the file path.
- CLI subcommands `init`, `check`, `list`, `get`, `eval`, `serve`,
  `version` with documented exit codes (0/1/2/3).
- Runnable examples (`examples/flags.toml`, `examples/poll.sh`) and a
  full file-format + evaluation-semantics reference
  (`docs/file-format.md`).
- 85 deterministic offline tests (parser, validation, bucketing,
  evaluation, HTTP API via httptest, in-process CLI) and
  `scripts/smoke.sh`.

[0.1.0]: https://github.com/JaydenCJ/flagstead/releases/tag/v0.1.0
