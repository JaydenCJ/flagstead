# The flagstead file format

One TOML file holds everything: flags, targeting rules, variants and
remote config. This document is the normative reference for the format
and for evaluation semantics. `flagstead check` enforces all of it.

## Top level

```toml
version = 1        # optional; must be 1 when present

[flags.<name>]     # zero or more flag tables
[config.<any>]     # one free-form config tree
```

Any other top-level key is a hard error. Flag names may contain letters,
digits, `.`, `_` and `-` (they appear in URLs).

## Flags

```toml
[flags.new-checkout]
description = "New checkout flow"   # optional
enabled = true                      # REQUIRED — flags are explicit
rollout = 25                        # optional, 0–100, default 100
salt = "new-checkout-v2"            # optional, default = flag name
tags = ["checkout", "q3"]           # optional
```

`enabled` is required on every flag: a flag whose state you have to
guess is a production incident waiting to happen. Unknown keys are hard
errors, so `enbled = true` cannot silently ship.

`rollout` accepts fractions (`0.25` = 25 basis points); resolution is
1/100 of a percent. `salt` exists so you can restart an experiment
(change the salt, everyone re-buckets) or share a bucket population
between two flags (give them the same salt).

## Rules

```toml
[[flags.new-checkout.rules]]
attribute = "country"   # REQUIRED — which context attribute to test
op = "in"               # REQUIRED — see the operator table
values = ["JP", "DE"]   # or value = "JP" for single-value operators
enabled = true          # optional outcome override; false forces OFF
variant = "treatment"   # optional forced variant (must be declared)
rollout = 100           # optional per-rule rollout override
```

Operators and their arity:

| Op | Values | Matches when |
|---|---|---|
| `eq` / `ne` | 1 | attribute equals / does not equal the value |
| `in` / `not_in` | ≥1 | attribute is / is not one of the values |
| `contains` | 1 | attribute contains the value as a substring |
| `prefix` / `suffix` | 1 | attribute starts / ends with the value |
| `gt` `gte` `lt` `lte` | 1 | both sides parse as numbers and compare true |
| `exists` / `not_exists` | 0 | attribute is present / absent |

A missing attribute matches only `not_exists` — never `ne` or `not_in`.
This "fail closed" rule means a typo'd attribute name can never
accidentally target everyone. Non-numeric attribute values simply do not
match numeric operators; evaluation never errors.

## Variants

```toml
[[flags.cta-copy.variants]]
name = "control"
weight = 75

[[flags.cta-copy.variants]]
name = "action"
weight = 25
```

Weights are relative (75/25 behaves like 3/1); the total must be > 0. A
zero-weight variant is legal and is never picked — use it to pause an
arm without invalidating assignments. Declaration order matters for
bucket layout, so append new variants rather than reordering.

## Config

Everything under `[config]` is served verbatim as JSON at `/v1/config`
and addressable by path: `[config.api] timeout_ms = 500` becomes
`GET /v1/config/api/timeout_ms` → `{"value": 500}`.

## Evaluation semantics (normative)

For `Evaluate(flag, context)` where context = (key, attributes):

1. If `enabled = false` → **off**, reason `flag_disabled`. No exceptions.
2. Rules run in file order. The **first matching rule** decides:
   - `enabled = false` on the rule → **off**, reason `rule`.
   - Otherwise the effective rollout is the rule's `rollout` if set,
     else the flag's. If `bucket(salt, key)` falls outside it → **off**,
     reason `rollout`. Else → **on**, reason `rule`, with the rule's
     `variant` if set, else a weighted pick.
3. No rule matched: the flag's rollout gates the same way — **on/off**
   with reason `rollout`, or reason `default` when rollout is 100.
4. Every **on** result for a flag with variants carries one variant.

## Bucketing (normative)

```text
bucket(salt, key) = FNV1a64(salt || 0x00 || key) mod 10000
in_rollout        = bucket < percent * 100
variant point     = bucket(salt || 0x00 || "variant", key) / 10000 * total_weight
```

- Deterministic: no state, no randomness, identical on every platform
  and across server restarts — two servers reading the same file give
  the same answers.
- Sticky: the bucket never changes, only the threshold moves, so raising
  a rollout never disables an already-enabled key, and rolling back
  disables exactly the most recently added keys.
- Independent: the variant hash is salted differently from the rollout
  hash, so the rollout population is not correlated with the arm split.

## The TOML subset

flagstead parses TOML with a built-in zero-dependency parser covering:
tables, arrays of tables, dotted and quoted keys, basic and literal
strings with escapes, integers (decimal, hex, octal, binary,
underscores), floats, booleans, arrays (multiline, trailing comma) and
inline tables. Deliberately **not** supported, each rejected with a
targeted error: date/time values and multi-line strings — neither has a
sensible meaning in a flag file. Every parse error carries a 1-based
line number.
