---
sidebar_position: 2
title: The Cluster Lock
---

# The Cluster Lock

Every `dcctl` run that changes a cluster takes a **lock** on that cluster before it
applies anything, and holds it until the run ends. Two operators on two machines can
no longer apply to the same cluster at the same time without one of them being told.

This page is for the person who has just been told the cluster is taken.

## "This cluster is claimed by…" {#claimed}

The message comes in two forms, and the difference between them is the only thing you
need from it at first:

```
this cluster (working on instance "prod") is being worked on by
alice@build-01/48213/9f3c1a20b7e4d5c6, which renewed 4s ago; wait for it to finish
rather than running a second apply against the same cluster
```

Someone is running `dcctl` against this cluster **right now**. Wait for them.

```
this cluster (working on instance "prod") is claimed by
alice@build-01/48213/9f3c1a20b7e4d5c6, which last renewed 6m12s ago — by this
machine's clock that looks stale, but a clock that disagrees looks the same. If that
process is genuinely gone, take the claim with `dcctl instances reclaim
--kube-context prod-cluster`, which checks properly before it steals
```

The lock has not been renewed for a while. That is **evidence, not a verdict** — the
timestamp was written by another machine's clock and is being read by yours, so the
two can disagree by more than the gap you are looking at. Deciding the holder is
really gone is [`dcctl instances reclaim`](#reclaim)'s job, and it uses a test that
does not depend on the two clocks agreeing.

The holder string names a **user**, a **machine**, a **process id** and a random
suffix — `alice@build-01/48213/…`. The first three are things you can go and check;
the suffix is there so that a second run by the same user on the same host cannot be
mistaken for the first one.

### Which commands refuse, and which warn {#refuse-or-warn}

| Command | If the cluster is claimed by someone else |
|---|---|
| `dcctl bootstrap` | **Refuses**, at its second step — before the operator, the infrastructure or the chart are touched. |
| `dcctl destroy` | Warns and continues — *"but if that run is live, this will fight it"*. |
| `dcctl upgrade` | Warns and continues, with the same warning. |
| `dcctl bootstrap --dry-run` | Takes no lock at all, and reports the claim it *would* have met. |

(The lock is taken at the *second* step, not the first, because the step before it is the
`--build` developer path's image build, which needs no cluster lock and produces the image
the operator step then deploys. On the published-image path that step does nothing at all.)

The asymmetry is deliberate. A second bootstrap running alongside a first produces one
instance built half from each, and refusing is the only useful answer. A teardown is
something an operator has already decided to do, often because something is wrong, and
being unable to ask the cluster politely first must not be what stops them — so those
two say so loudly and carry on.

A dry run is a plan, and a plan that mutates the cluster is not one. It writes nothing,
including the lock. It still reports who holds it, because "someone else is already
running" is part of the answer to "what would this do".

## What the lock actually covers {#scope}

**One lock per cluster — not one per instance.** Almost everything a bootstrap touches
is a cluster-wide singleton: the Helm release, the infrastructure releases that install
the ingress controller, cert-manager and the CloudNativePG operator, and the DeviceChain
operator's own Deployment. Two runs working on two *different* instances would still
overwrite each other's copies of all of it. The instance id is recorded on the lock so
the refusal can tell you which instance the holder is working on, but it is not what the
lock is keyed by.

:::caution This enforces "one run at a time", not "one instance per cluster"
The lock stops two `dcctl` processes from applying at once. It does not make a cluster
safe to host two DeviceChain instances — today a cluster holds one, and that remains
true whether or not anyone is holding the lock.

That boundary is held by a different check, one step later: the bootstrap asks the
cluster whether it already holds a *different* instance and refuses if it does, whatever
the lock says. See [One instance per
cluster](./bootstrap.md#what-it-does).
:::

The lock is a Kubernetes `Lease` named `dcctl`, in the namespace the DeviceChain
operator installs itself into (`dc-k8s-system`). You can read it directly:

```bash
kubectl --context <kube-context> get lease dcctl -n dc-k8s-system -o yaml
```

It is valid for **60 seconds** after its last renewal, and the holder renews every
**10 seconds** — a wide enough margin that one slow API call is not mistaken for a dead
process. A run that finishes, fails, or is interrupted **deletes** the lock on its way
out rather than leaving it to expire, so the ordinary case costs the next operator
nothing.

### Permissions {#rbac}

`dcctl` acts as the person running it, so on a cluster somebody else administers your
account needs:

- `get`, `create`, `update` and `delete` on `leases.coordination.k8s.io` in
  `dc-k8s-system`;
- `get`, `create` and `update` on `instances.core.devicechain.io`, cluster-wide.

If the account does not hold them, `dcctl` surfaces the API server's own refusal —
which verb, which resource, which namespace, which user — rather than reporting it as
an outage.

## Reclaiming a lock whose run is gone {#reclaim}

A run that is killed without being able to give the lock back — a lost laptop, a dead
SSH session, an OOM kill — leaves the lock behind until somebody takes it.

```bash
dcctl instances reclaim --kube-context <kube-context>
```

`--kube-context` is not a convenience here. This command exists for the operator on a
*different* machine from the one that bootstrapped, and that machine has no local record
of the instance, so naming the cluster explicitly is the only way it can find the lock.

The command prints who holds it, which instance they were working on, and how long ago
they renewed. Then it asks you to **type the holder identity back**, exactly:

```
  held by:   alice@build-01/48213/9f3c1a20b7e4d5c6
  instance:  prod
  renewed:   6m12s ago

Type the holder identity above to take the lock, or anything else to abort:
>
```

Anything that does not match aborts and leaves the lock alone. A yes/no prompt would
add ceremony and no information — the answer is the same whether or not you read the
line above it — and the one real risk this command carries is taking a lock from a
process that is still alive. Typing the identity is what makes you look at whose it is.

Only then does it check:

```
checking whether the holder is still renewing (this takes about 1m0s)...
```

**The check is that nothing touched the lock over a window this machine timed.** It
reads the object, waits a full lease duration measured on *your* clock, and reads it
again. A live holder renews six times inside that window, so an object that has not
changed at all means no renewal reached the API server. Nothing compares two machines'
clocks, because any such comparison is wrong by the skew between them — and the
dangerous direction, declaring a live holder dead, is the one a drifting laptop clock
produces for free.

If the holder wakes up and renews during the window, or in the instant between the
check and the steal, the reclaim is **refused** rather than silently overwriting a live
claim.

On success the lock is handed straight back and the cluster is free:

```
the cluster lock is now free
```

Reclaim does not hold the lock for you. Run your `bootstrap`, `destroy` or `upgrade`
afterwards, as normal.

:::danger It cannot tell a dead process from a stopped one
A suspended VM, a closed laptop lid, a process stopped with `SIGSTOP` — all of them look
exactly like a crash from here, and no amount of waiting changes that. Nothing a second
machine can observe distinguishes the two. **Confirm the other process is really gone
before you take its lock**, by asking the person or looking at the machine; this command
can only prove that nothing has renewed, not that nothing will.
:::

There is deliberately **no `--yes`**. An unattended reclaim would need a judgement — *"I
know that process is gone"* — that no flag can carry.

If the lock is already free, the command says so and does nothing:

```
this cluster is not claimed; there is nothing to reclaim
```

### What happens to the run that was reclaimed {#fenced}

It finds out. The holder re-reads the lock every ten seconds and checks that it is still
its own, and `dcctl` re-checks at every step boundary as well, so a reclaimed run stops
before starting its next step:

```
stopping before "Apply infrastructure": this cluster was reclaimed by another
operator: it is now held by bob@laptop/9912/3a7f…
```

A fenced run writes nothing further to the cluster — not even the phase annotation on
its own instance declaration, because that declaration now belongs to whoever reclaimed
it.

:::caution A reclaim cannot interrupt a step already running
The check happens *between* steps. A reclaim that lands one second into "Apply
infrastructure" is not acted on until that step returns, and an infrastructure apply
can run for tens of minutes. The exposure is the remainder of the current step, not the
ten-second detection interval — which is the real reason a reclaim is slow, manual and
typed rather than automatic.
:::

The symmetry holds in the other direction too: a run that has been **unable to renew**
for a full lease duration — a network partition, an API server it can no longer reach —
declares itself lost and stops, rather than carrying on applying with confidence while
somebody else correctly concludes it is gone.

## Interrupting a run {#interrupt}

`Ctrl+C` stops a run **gracefully**. The infrastructure tool is asked to stop the way it
wants to — it finishes the operation in flight and writes its state file — and `dcctl`
gives the lock back before exiting. The next run, usually you retrying, finds the
cluster free.

Because a single Helm release inside an apply can carry a timeout measured in minutes, a
graceful stop is allowed to take a while: up to twenty minutes in the worst case, and
usually far less.

**A second `Ctrl+C` exits immediately.** That is the escape hatch for someone who has
decided that waiting for a clean stop is no longer worth it.

:::caution The second interrupt gives up both protections
It ends the process without the graceful stop, so the infrastructure tool can be killed
mid-apply and lose track of resources it had just created — and the lock is **not**
given back, so the next run against that cluster has to wait out a lease duration and
[reclaim](#reclaim) it. Use it when the first interrupt is not making progress, not as
the normal way to stop.
:::

## See also

- [Bootstrap an Instance](./bootstrap.md) — what each step of a run does.
- [Deployment & Operator](./kubernetes-operator.md#instance-declaration) — the instance
  declaration a run writes, the finalizer that protects it, and `dcctl instances
  release`.
