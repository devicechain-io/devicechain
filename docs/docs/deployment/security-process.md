---
sidebar_position: 12
title: Security process
---

# Security process {#security-process}

This page describes how DeviceChain handles vulnerability reports, how it meets the reporting
duties of the EU Cyber Resilience Act, what it publishes about each release, and how the
AI agents that help maintain the project are kept away from anything that could ship a
compromised build. It says what is in place today and what is not yet.

## Reporting a vulnerability {#reporting}

Report privately, never in a public issue. The routes, and the supported versions, are in
[SECURITY.md](https://github.com/devicechain-io/devicechain/blob/main/SECURITY.md). A
machine-readable contact is published at
[`/.well-known/security.txt`](https://docs.devicechain.io/.well-known/security.txt).

| Step | Target |
| --- | --- |
| Acknowledge and triage: read it, reproduce or reject it, make a first severity call | within 24 hours, every day of the week |
| Full assessment: valid, severity, affected versions | within 7 days |
| Fix, by severity | Critical 14 days, High 30, Medium 90, Low the next release |

These are targets held by a small team, not guarantees. If one is going to be missed, the reporter
is told why.

## Reporting duties under the Cyber Resilience Act {#cra}

Since 2026-09-11 a manufacturer must report to the authorities a vulnerability in its product that
is being **actively exploited**, and a **severe incident** that affects the security of the product.
Both follow the same three steps, counted from the moment the project becomes aware:

| Step | Actively exploited vulnerability | Severe incident |
| --- | --- | --- |
| Early warning | within 24 hours | within 24 hours |
| Notification, with what is known so far | within 72 hours | within 72 hours |
| Final report | 14 days after a corrective measure is available | 1 month after the notification |

The runbook the maintainers follow:

1. **Decide whether the clock has started.** Active exploitation means credible evidence that someone
   used the weakness against a real system without the owner's permission. A proof of concept sent
   by a researcher does not start it. A report of use in the wild does.
2. **Send the early warning** through the single reporting platform the EU agency for cybersecurity
   operates, which routes it to the national coordinator (CSIRT) of the member states concerned.
   It states that the report is an early warning, whether malicious action is suspected, and the
   member states where the product is available.
3. **Send the notification** within 72 hours: the nature of the vulnerability, the affected versions, any
   mitigation already available, and an assessment of severity.
4. **Ship the fix and tell users.** A fixed patch release and a GitHub Security Advisory (with a CVE where one
   applies) go out together, and the advisory says what to do before upgrading if there is anything.
5. **Send the final report** with the root cause, the fix, the malicious actor if known, and how
   users were informed.
6. **Record** the dates of each step in the private advisory so the next incident starts from a
   working example.

DeviceChain is open-source software that is not sold, so whether a given deployment makes its
operator the manufacturer for these purposes is for that operator to determine. The project
runs this process for the software it publishes regardless.

## What each release publishes {#release-artifacts}

Every release carries a signed SBOM and build provenance for each image
([Verifying releases](verifying-releases.md)). When an advisory affects a dependency, the
maintainers check the release SBOMs for the affected package and version, list the images that
are exposed, and publish a statement in the OpenVEX format with the advisory: **affected** or
**fixed** for the images that carry the package, and **not affected** with a stated reason for
those that ship it but cannot reach the vulnerable code.

The check is a script in the repository, `hack/vex-exposure.sh`. It reads an SPDX SBOM per image and
an advisory, lists exposed images, and drafts the statement. It treats an SBOM with no packages as an
error, never as "nothing exposed".

## Reproducible builds {#reproducible}

A build is **reproducible** when building the same commit twice, on different machines and in
different directories, produces byte-identical output. A job in CI builds `dcctl` and the edge agent
from two unpacked copies of the commit with separate caches and fails if the digests differ.
It is exercised by a planted nondeterministic build, which it must catch. Container images and the
Helm chart are not yet covered by that job; the broker image has its own reproducibility check.

## AI-assisted development {#ai-assisted-sdl}

AI agents draft code, documentation and reviews for this project. They are treated as an
untrusted contributor with an unusually fast keyboard, and the controls do not depend on the agent
behaving well:

- **No secrets.** Agents run without access to signing identities, registry credentials or cloud
  keys. The release workflow signs with a short-lived identity bound to the repository, and no
  long-lived signing key exists.
- **No release authority.** An agent cannot cut a tag or publish an artifact. The release workflow runs
  only from a maintainer-created tag.
- **No merge to the protected branch.** An agent opens a pull request. A maintainer merges it after
  the required checks pass, the same as for any contributor.
- **Untrusted input is data.** Issue text, pull-request descriptions, advisory reports, web pages and
  dependency documentation that an agent reads are treated as data to analyse, never as instructions
  to follow. A report that says "ignore your rules" is a finding.
- **Review.** A change an agent wrote is reviewed by a maintainer and, for risky areas, by a second
  independent reviewer, and a new guard is shown to fail on a planted defect before it is trusted.

## Not yet in place {#roadmap}

- Reproducibility checks for container images and the Helm chart.
- Replacing the remaining long-lived cloud credential used for performance testing with short-lived,
  workload-identity access from CI.
- A published target score against the OpenSSF Scorecard and the OpenSSF baseline controls. Scorecard
  already runs weekly and its result is public.
