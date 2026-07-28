# drdrill — the root-key restore drill

`drdrill` is the measuring instrument for the ADR-028 disaster-recovery claim. It
does not run the drill; [`hack/dr-rig.sh`](../../../hack/dr-rig.sh) does. It
answers the one question the rig cannot answer from the outside:

> Is the secret this cluster is serving the same secret the *previous* cluster
> sealed — and can this cluster still open it?

## The two halves

```
drdrill seed   --instance <id> --receipt <path> [--server host[:port]]
drdrill verify --receipt <path> --db-host ... --db-user ... [--skip-api]
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
| `4` | the row is not there; the restore did not happen |
| `5` | it decrypted and the plaintext is wrong — corruption, not a key problem |

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

## Scope

This is the **core-data** drill, which is a seam and not a shortcoming.

An instance's data lives on two separate database servers. The relational one
holds tenants, devices, profiles, rules, dashboards, connectors, last-known state
and every stored secret; TimescaleDB holds event history. `event-management` is
the only service that talks to TimescaleDB and it talks to nothing else, so the
two have no cross-writes to keep consistent — they are two backups and two
restores, with different sizes, cadences and recovery-time targets.

The root key gates the core half **only**: event data holds no ciphertext. So the
core-data restore is exactly the operation the escrow artifact exists to make
possible, and the one that had to be proved first. The event-data restore is a
sibling drill this tool does not attempt, as are JetStream state and object
storage.
