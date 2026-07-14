# flagstead examples

Two runnable pieces, both offline and self-contained.

## flags.toml

A realistic mid-sized flag file: a revenue-critical flag at 10% rollout
with a staff-allowlist rule and a force-off rule for old app versions, a
three-arm copy experiment, a retired flag, and a remote-config tree.

```bash
flagstead check --file examples/flags.toml
flagstead eval checkout-v2 --file examples/flags.toml \
  --key user-42 --attr email=dev@example.test
flagstead serve --file examples/flags.toml
```

Try flipping `enabled` on `checkout-v2` while the server runs — the next
poll picks it up, and `git diff` shows exactly what changed.

## poll.sh

An ETag-polling client in ~20 lines of shell + curl — the same loop an
SDK's background refresher runs. Start the server, then:

```bash
bash examples/poll.sh http://127.0.0.1:4949 2
```

While the file is unchanged every tick prints a bodyless `304`; edit the
file and the next tick prints `200` with the new snapshot. Cost of
polling when nothing changed: one conditional GET, zero bytes of body.
