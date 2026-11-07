# Transport conformance

Every filled circle on the [transport matrix](../../docs/docs/reference/transport-matrix.md) is the result
of a test against a client somebody else wrote. This directory is how.

| File | What it is |
| --- | --- |
| `../conformance-rig.sh` | The rig: `up` makes a kind cluster and a default instance from this tree, `run` drives it, `down` removes it |
| `steps.sh` | The checkers, the negative controls and the scenarios; sourced by the rig |
| `matrix.json` | The spec: the checks, the negative controls, and which cell of which table rests on which checks |
| `results.json` | What the last run found: the commit, the time, every check's verdict and every control's |
| `client/` | The Paho (MQTT 3.1.1) and Tahu (Sparkplug B) clients, built in a container |
| `mosquitto.yaml` | The operator-owned broker the Sparkplug scenarios read from |
| `../check-transport-matrix.sh` | The CI guard and the renderer |

The Leshan (LwM2M) client is the interop client already in `backend/services/lwm2m-ingest/interop`; the rig
builds it from this tree.

## What is checked, and how

| Client | Scenario | Judged by |
| --- | --- | --- |
| Paho | QoS 0 and QoS 1 publish; a command subscription | the event store, the command record, `event-sources`' counters |
| curl | HTTP ingest: 202, 429, 503 with `Retry-After` | the status line and header, the event store (a refused event must be absent), the refusal counters |
| Leshan | register, observe, read, write, execute, the named reboot and device-information commands, the registration-time harvest | `device-state`, the event store, the command record, the device's client attributes |
| Tahu | a node's birth and data, by alias | `device-state` and the event store |

A client's own account of what it did is never the evidence. A client that lies, or does nothing, cannot
manufacture a pass.

## Negative controls

A check is worth nothing until it has been shown to fail, and the suite does not count until every control
has turned the check it names red. There are two kinds.

- **Planted defects**, run against the live instance:
  - `N1` a dropped command subscription: the device connects and never subscribes;
  - `N2` a decoder off-by-one: a Sparkplug `NDATA` one alias past the one the birth gave `temperature`;
  - `N3` LwM2M data a 1.0-style client can only encode as TLV, which must fail the observe check while the
    limit check, "such a client yields no telemetry", stays green. That pair is what keeps the matrix cell the
    honest half-filled circle.
- **Record controls**: each checker is a pure function of a record of what was read, so the rig hands it a
  fabricated record of the opposite verdict, offline and first (`hack/conformance-rig.sh controls`).

## Running it

```bash
hack/conformance-rig.sh all      # up + run: about half an hour, most of it building images
hack/conformance-rig.sh down
```

It needs `kind`, `kubectl`, `helm`, `docker`, `jq`, `openssl`, `curl`, `ko`, `tofu` or `terraform`, and Go. The
instance is built from the checked-out commit, and `run` refuses to start on an instance built from another.
Run it on a **clean, committed tree**: the results name the commit they describe, and a file made on a dirty
tree is refused by the renderer.

Then render the matrix and commit both files:

```bash
hack/check-transport-matrix.sh render
```

## What CI checks

`hack/check-transport-matrix.sh` (the `docs` job) re-renders the three pages from `matrix.json` and
`results.json` and fails on any difference, so a hand-edited cell fails in each locale. It also refuses results
that were made on a dirty tree, that cover a different set of checks or controls than the spec declares, or
in which a control did not go red. It does not check how old the results are, because a documentation change
must not fail on the date of the last rig run. Before a release candidate, run:

```bash
hack/check-transport-matrix.sh release
```

which also refuses results older than `DC_CONFORMANCE_MAX_AGE` (45 days by default) and results whose commit is
not in this tree's history. Changing a check or a control changes the digest recorded in the results, so the
rig has to be run again; changing only a cell does not.

## Changing the matrix

- A **filled circle** needs `evidence`: the checks that substantiate it. It cannot rest on prose.
- A **half-filled circle** may list `limits`: checks that pass while the stated limitation still holds. When
  one fails, the limitation is gone and the renderer refuses until a person looks at the cell.
- A cell the rig does not test says `"basis": "prose"`, so an untested cell is a visible decision.
- The prose around the tables is still written by hand, in every locale.
