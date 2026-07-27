---
sidebar_position: 5
title: Disaster Recovery
---

# Disaster Recovery

Restoring a DeviceChain instance takes **two** things: a backup of its databases, and
the instance's **secret-store root key**. Almost every backup procedure captures the
first and silently omits the second.

This page is about the second one.

## Why the root key needs its own procedure

Every secret DeviceChain stores on your behalf — outbound-connector credentials, SMTP
passwords, AI provider keys — is encrypted at rest under a per-secret data key, and
each of those data keys is wrapped by one instance-wide **root key** (the KEK, see
[ADR-059](../concepts/architecture.md)).

That root key lives in the instance's Kubernetes Secret, which means it lives in
**etcd**. Nothing DeviceChain backs up contains etcd. A CNPG backup archives
PostgreSQL WAL; a TimescaleDB backup covers TimescaleDB. Neither contains a single
byte of the key.

The consequence is a failure that passes the drill most people actually run:

- **Restore the databases in place** — into the same cluster — and everything works,
  because etcd still holds the key. This is the rehearsal that gives false
  confidence.
- **Restore to a fresh cluster** — the actual disaster — and the encrypted rows
  rehydrate perfectly, the restore reports success, and every one of those secrets is
  permanently unreadable. The new cluster minted a *different* root key, and the old
  one is not derivable from anything you still have.

The failure does not surface at restore time. It surfaces later, as an unexplained
decryption error, typically long after the backup that could have helped has rotated
away.

:::danger There is no recovery from a lost root key
The key is 256 bits of randomness and the wrapped data keys are not brute-forceable.
If the key is gone, the secrets are gone — a support ticket cannot recover them. This
is the one piece of DeviceChain state with no second chance, which is why the escrow
below is on by default.
:::

## The escrow artifact

`dcctl bootstrap` writes an **encrypted escrow artifact** — a small text file
containing the root key, sealed under a passphrase you choose:

```
~/.devicechain/escrow/<instance>-rootkey.escrow
```

It is a self-describing text file. Opened years later by someone who has never seen
one, it explains what it is, what it protects, what happens if it is lost, and the
exact command to recover with — without needing this page.

Two properties are worth knowing:

- **It is not stored with the instance.** It deliberately does *not* live in
  `~/.devicechain/<instance>/`, because [`dcctl destroy`](#after-destroy) removes that
  directory. `dcctl` refuses an `--escrow-file` path inside it.
- **It carries a key fingerprint in the clear.** That is what makes "is this escrow
  still the right one?" a question you can answer *without* the passphrase — see
  [verifying](#verify).

### Choosing a passphrase

Bootstrap takes the passphrase from the first of these it finds:

| Source | Use it for |
|--------|-----------|
| `--escrow-passphrase-file <path>` | Automation with a secret manager; a trailing newline is stripped. |
| `DCCTL_ESCROW_PASSPHRASE` | CI and scripted installs. Set-but-empty is an error, not a fallback. |
| Interactive prompt | A person at a terminal. Asked twice, to catch a typo now rather than during a recovery. |

If none is available and there is no terminal to ask at, **bootstrap fails**. That is
deliberate: the alternative is quietly producing an instance whose secrets die with
its cluster.

:::caution Store the file and the passphrase separately, and off the cluster
Both in the same place is one compromise away from being no protection at all, and
both on the cluster is one disaster away from being no backup at all.
:::

### Opting out

For a genuinely disposable instance — a CI run, a demo, a local experiment — pass
`--no-escrow`. `--dev` implies it.

```bash
dcctl bootstrap local scratch --dev            # no escrow, disposable by construction
dcctl bootstrap local scratch --yes --no-escrow
```

The bootstrap summary then says so in red. Do not use it for anything whose secrets
you would miss.

## Recovering an instance {#recover}

The order matters. Seed the key **first**, restore the data **second** — an instance
built on the wrong key will write new secrets under it, and those become collateral
damage when you fix the key afterwards.

**1. Rebuild the instance with the escrowed key.**

```bash
dcctl bootstrap local my-instance --restore-root-key ~/backups/my-instance-rootkey.escrow
```

You will be asked for the artifact's passphrase (or supply it with
`--escrow-passphrase-file` / `DCCTL_ESCROW_PASSPHRASE`). The new instance now runs the
*same* root key the old one did.

**2. Restore the databases** into that instance, using your normal PostgreSQL and
TimescaleDB restore procedure.

**3. Confirm the stored secrets decrypt** — read back a secret-backed object (an
outbound connector, a notification channel) through the console or the API. A restore
that returns rows is not proof; a value that decrypts is.

:::note Restoring under a different instance name
Perfectly supported — the artifact records the name it was written for and `dcctl`
notes the mismatch rather than refusing. The recorded name is authenticated, so it
cannot be edited without invalidating the file.
:::

## Verifying your escrow, before you need it {#verify}

An escrow that no longer matches the running key is indistinguishable from a good one
until the day it is used. Check it on an ordinary Tuesday:

```bash
dcctl secrets escrow verify ~/backups/my-instance-rootkey.escrow --instance my-instance
```

This compares the artifact's fingerprint against the key the instance is **actually
running**, needs no passphrase, and exits non-zero on a mismatch — so it composes into
a cron job or a CI gate. A mismatch means the instance has no usable escrow, most
often because it was re-bootstrapped after the file was written.

To see what an artifact is without opening it:

```bash
dcctl secrets escrow show ~/backups/my-instance-rootkey.escrow
```

:::caution What `verify` does not prove
It proves the artifact names the right key. It does not prove the artifact still
*opens* — that needs the passphrase. Rehearse a real recovery periodically; a
fingerprint check is a smoke alarm, not a fire drill.
:::

## Re-running bootstrap on a live instance

`dcctl bootstrap` is idempotent, and re-running it against an existing instance
**reuses that instance's existing root key** rather than minting a new one. If it
cannot determine whether the instance exists, it stops rather than guessing — minting
would be the destructive answer.

On a re-run it also reconciles the escrow:

- artifact matches the running key → confirmed, left untouched;
- artifact does **not** match → the run stops and names it as an orphan;
- no artifact → one is written, so an instance first created with `--no-escrow` can
  gain an escrow later.

## After `dcctl destroy` {#after-destroy}

`dcctl destroy` removes the cluster and the instance's local state — but **not** the
escrow artifact, which lives outside that directory by design, and which destroy names
on its way out.

Keep it for as long as you keep any backup of that instance's databases. It is the
only thing that can still read them. Delete it when those backups are gone, and not
before.
