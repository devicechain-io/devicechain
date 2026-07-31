---
sidebar_position: 6
title: Getting help
---

# Getting help

DeviceChain is pre-1.0 and developed in the open. If something did not work, or a page
did not tell you what you needed, we want to hear about it — early and rough beats late
and polished, because interfaces can still change in response to what people run into.

## Where to go

| What you have | Where it goes |
| --- | --- |
| A question, or something that confused you | [Discussions](https://github.com/devicechain-io/devicechain/discussions) |
| An idea or feature request | [Discussions → Ideas](https://github.com/devicechain-io/devicechain/discussions/categories/ideas) |
| Something is broken | [Open an issue](https://github.com/devicechain-io/devicechain/issues/new/choose) |
| A security vulnerability | Email **admin@devicechain.io** — please do not file publicly |

You do not need to be sure it is a bug before saying something. If you could not tell
whether the behaviour you saw was intended, that ambiguity is itself worth reporting.

## Filing a good issue

The issue forms ask for what we would otherwise have to come back and request. The two
that save the most time:

**Which version.** Run `dcctl version`, or give the chart or image tag you installed.

**Where it stopped.** Not the whole story — just the last step that worked and the first
that did not. For data that never arrives, that means saying whether the device connected,
whether events were recorded, and whether the console showed anything, in that order.

For install problems, the environment matters more than usual, because bring-up is the
part we can least easily reproduce — we only have our own machines. Include your
Kubernetes distribution and version, your host OS and CPU architecture, and the output of
`kubectl get pods -A`. `dcctl preflight` catches many environment problems on its own and
its output is worth pasting even when it passes.

:::caution Redact before you paste
Logs and command output can carry tokens, connection strings, and internal hostnames.
Issues and Discussions are public.
:::

## Things that silently swallow data

If telemetry is missing, these three account for most reports, and ruling them out first
will usually be faster than waiting on a reply:

- **The device is not assigned.** Events from an unassigned device are dropped without an
  error anywhere the sender can see.
- **The measurement is not on the profile.** A device type's profile declares the
  measurements the platform accepts; anything else is discarded during decoding.
- **You are looking at a different tenant** than the one the device reports into.

## What to expect

DeviceChain is maintained by a small team, so a reply may take a few days — an issue
sitting unanswered for a little while has not been ignored. Reports that include a
version and a clear stopping point get resolved fastest, because they need no round trip
before anyone can start looking.
