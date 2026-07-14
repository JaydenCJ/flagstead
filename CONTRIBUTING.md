# Contributing to flagstead

Issues, discussions and pull requests are all welcome.

## Getting started

You need Go ≥1.22 and (for the smoke test) curl; nothing else.

```bash
git clone https://github.com/JaydenCJ/flagstead && cd flagstead
go build ./...
go test ./...
bash scripts/smoke.sh
```

`scripts/smoke.sh` builds the binary, generates a flag file, exercises
every CLI subcommand, then runs a real server on a loopback port and
asserts on ETag polling, hot reload and the broken-edit safety net; it
must finish by printing `SMOKE OK`.

## Before you open a pull request

1. `gofmt -l .` reports nothing (formatting is enforced).
2. `go vet ./...` passes with no findings.
3. `go test ./...` passes (85 deterministic tests, no network).
4. `bash scripts/smoke.sh` prints `SMOKE OK`.
5. Add tests for behavior changes; keep logic in pure, unit-testable
   modules (parsing, validation and evaluation never touch the network —
   only `server` and the CLI do I/O).

## Ground rules

- Keep dependencies at zero — flagstead is standard library only, and
  that is the point of the project. Adding one needs a very strong case.
- No network calls except serving the user's own HTTP API on the address
  they chose (loopback by default). No telemetry, ever.
- Evaluation must stay a pure function: identical (file, context) inputs
  must produce identical results on every platform, forever. Changing
  the bucketing hash or salt scheme is a breaking change to every
  in-flight rollout and needs a migration story.
- Validation is strict by design: new file keys must be validated, and
  unknown keys stay hard errors — silence is how flags mis-fire.
- Code comments and doc comments are written in English.

## Reporting bugs

Include the output of `flagstead version`, the exact command or HTTP
request, the relevant slice of your flag file (redact values if needed),
and — for evaluation surprises — the full JSON result, since `reason`,
`rule_index` and `bucket` say exactly which branch decided.

## Security

Please do not open public issues for security problems; use GitHub's
private vulnerability reporting on this repository instead.
