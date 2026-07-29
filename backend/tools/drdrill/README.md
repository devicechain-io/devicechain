# drdrill — the root-key restore drill

`drdrill` is the measuring instrument for the ADR-028 disaster-recovery claim. It
does not run the drill; [`hack/dr-rig.sh`](../../../hack/dr-rig.sh) does. It
answers the one question the rig cannot answer from the outside:

> Is the secret this cluster is serving the same secret the *previous* cluster
> sealed — and can this cluster still open it?

## The two halves

```
drdrill seed   --instance <id> --receipt <path> [--server host[:port]]
drdrill verify --receipt <path> --db-host ... --db-user ...
drdrill decoy  --instance <id> --out <path>
```

**`seed`** writes a secret through the platform's public GraphQL API: it creates a
tenant, a scoped tenant-admin identity, and a notification channel carrying a
freshly random delivery secret. That routing — ingress, tenant token, the deployed
`notification-management` — is the point. The ciphertext the backup then carries
was sealed by the real service under the KEK the chart handed it, not
manufactured by this tool.

It writes a **receipt** (mode `0600`): the tenant, the secret handle, the channel,
the scoped identity, and the expected plaintext. The plaintext is in the receipt
deliberately — see the type comment in [`receipt.go`](receipt.go). A fixed literal
shared by both halves would let a row left over from an earlier run satisfy this
run's comparison, which is precisely the false pass a restore drill must not be
able to produce.

**`verify`** proves the secret survived, in two steps that are kept separate on
purpose:

1. **over the API** — the restored instance still lists the channel and reports
   it holds a secret. This establishes that the restore landed and the service is
   up, so a later decrypt failure has only one explanation left.
2. **through `core/secrets`** — the row is resolved with the identical decrypt
   path the service uses, keyed by the root key the *rebuilt* cluster carries.

It does not read the secret back through the platform because the platform has no
such API by design (ADR-059: cleartext never crosses the API boundary), and
inventing one for a drill would be a much worse trade than reading the store.

**`decoy`** mints a well-formed escrow artifact holding a root key that is *not*
the instance's — the wrong key the negative control recovers under. It exists
because the control it replaces can no longer be expressed: `dcctl` refuses
`--restore-rdb-from` without `--restore-root-key`, which is the guard against an
operator silently losing every secret, and it takes "rebuild with `--no-escrow`"
with it.

The artifact comes out of `escrow.Wrap` — the same function `dcctl` calls, same
KDF parameters, same passphrase — and is consumed by the same
`--restore-root-key` path a real artifact takes, so the bootstrap cannot tell the
two apart. Minting one through the shipped CLI instead would mean bootstrapping a
throwaway instance purely to make it escrow a key, since there is no `dcctl
secrets escrow create` — a third cluster for one control, which is enough friction
that the control stops being run.

What it does not stand in for is any claim that two real bootstraps mint
*different* keys from each other; that belongs where the key generator lives. The
claim under test here is the one that matters in an incident: given the right
archive and the wrong key, does the platform hand over the plaintext?

It refuses to overwrite an existing file (`O_EXCL`, mode `0600`). Pointing `--out`
at the drill's real escrow artifact is the one mistake with no recovery.

## Exit codes are the interface

The negative control is only meaningful if "could not decrypt" is distinguishable
from "could not connect".

| Code | Meaning |
|------|---------|
| `0` | the secret decrypted and matched |
| `1` | **inconclusive** — bad flags, no database, no root key, an API that would not answer. Never a verdict about the data. |
| `3` | **the row is present and this instance's root key cannot open it** — the negative control's one acceptable outcome |
| `4` | the row is not there; the restore did not happen. Also the EVENT half's control outcome: the telemetry is absent and was reported absent |
| `5` | it decrypted and the plaintext is wrong — corruption, not a key problem. In the event half, history that came back incomplete or changed |
| `6` | event half only: the rows are right and TimescaleDB's own machinery is not. Separate from `5` because the two demand opposite responses — `5` says restore again from an earlier point, `6` says the data is fine and the cluster is not |

Code 3 is reached only after check 1 passed and the row was counted, and a resolve
error is re-tested against a live database before it is called a decrypt failure.
Without that, a dropped connection in the control run would read as "the key did
not fit" and the control would hold while testing nothing.

## The root key

`verify` takes it from `--root-key-file` or `$DRDRILL_ROOT_KEY`, never a flag — a
flag value is visible in the process table. An absent or blank key is refused
rather than passed through: a zero-length key fails the decrypt, which is exactly
the control's expected outcome, so an operator who simply forgot to export it
would otherwise record a control that "held".

## The event half

An instance's data lives on two separate database servers. The relational one
holds tenants, devices, profiles, rules, dashboards, connectors, last-known state
and every stored secret; TimescaleDB holds event history. `event-management` is
the only service that talks to TimescaleDB and it talks to nothing else, so the
two have no cross-writes to keep consistent — they are two backups and two
restores, with different sizes, cadences and recovery-time targets.

The root key gates the core half **only**: event data holds no ciphertext. So the
core-data restore is the operation the escrow artifact exists to make possible,
and the one that had to be proved first. `seed-events` and `verify-events` are the
other half, because "we can restore" is false as a claim if half of it has never
been tried.

```
drdrill seed-events   --receipt <path> --db-host ... --db-user ... [--reset]
drdrill verify-events --receipt <path> --db-host ... --db-user ... --server ...
```

They share the secret half's receipt, under the same tenant, so a receipt from one
disaster cannot satisfy the other half's verify.

**The event half checks the MACHINERY, not just the rows**, and that is the whole
reason it is not the secret half with different nouns. TimescaleDB has several ways
to come back wrong while every ordinary check reports success:

| Failure | Why a row count misses it |
|---|---|
| `shared_preload_libraries` loses `timescaledb` | the extension's catalog rehydrates with the data; the resource manager loads at server START. Measured: Cluster reports healthy, API answers, and **every row in a compressed chunk returns as nothing at all**, silently |
| a hypertable returns as a plain table | it holds every row, so counts agree; the partitioning is gone and cannot be restored in place |
| a continuous aggregate returns as a definition with no materialization | reads through the view still work |
| the background jobs come back unscheduled | job rows are DATA, so they restore whether or not anything will run them again |

So `verify-events` asks about the server's configuration **before** it asks about
rows — a precondition has to be answered before any verdict about data, or the
symptom outranks the cause. It reads the aggregate's materialization hypertable
directly rather than through the view, because the view's answer depends on the
watermark and the watermark is restored state too. And it seeds a
pre-compression-window cohort: the platform compresses every event hypertable
after 7 days, so an instance older than a week is mostly compressed chunks, and a
drill that only seeds fresh data restores the shape production mostly is not.

The event half's negative control is not "the wrong key" — there is no key. It is
**the verifier's ability to report absence**: the control cluster's event store is
not restored at all, so `verify-events` must return `4`. Until that has been seen,
a `verify-events` that always passed would be indistinguishable from a restore that
always worked.

JetStream state and object storage remain undrilled.
