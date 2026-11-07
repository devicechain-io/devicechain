---
sidebar_position: 4.6
title: Long-term support policy
---

# Long-term support policy {#long-term-support-policy}

DeviceChain designates **one release line at a time** as the long-term-support (LTS) line. An LTS line
receives **security fixes and critical fixes only**, as patch releases, for **18 months** from the date the
line is designated. This page says what that promises, how a fix reaches the line, and how you move off
it. It complements the [versioning and upgrade policy](./versioning-policy.md), which still governs every
release, LTS or not.

**No line is designated yet.** When one is, this page names it, and the repository records it in a single
declaration that the release and backport tooling read. Until then every release follows the ordinary
[versioning model](./releases-and-upgrades.md#versioning-model).

## What an LTS line receives {#what-it-receives}

| Receives | Does not receive |
| --- | --- |
| Fixes for security vulnerabilities. | New features, new fields or operations. |
| Fixes for critical defects: data loss or corruption, a platform that will not start or upgrade, an authentication or tenant-isolation failure. | Performance work, refactors, dependency bumps that are not themselves a security fix. |
| Patch releases (`1.4.1`, `1.4.2`) tagged from the line's own branch. | A new minor version. A new minor starts a new line on the main branch. |

A fix is proposed for the LTS line only when the change that made it carries the `security` or
`critical` label. Nothing else is proposed, and a proposal is never merged automatically.

## How a fix reaches the line {#how-fixes-arrive}

The LTS line `X.Y` lives on a branch named `release-X.Y-lts`, cut from the `vX.Y.0` tag.

1. A fix lands on the main branch first. A fix that exists only on the LTS branch is not accepted.
2. If it is labelled `security` or `critical`, a bot opens a pull request that cherry-picks it onto the LTS
   branch. If the commit does not apply cleanly, the bot says so on the original pull request and a
   maintainer backports it by hand.
3. That pull request runs the full CI matrix against the LTS branch, including the upgrade drill, and a
   maintainer reviews and merges it like any other change.
4. Patch releases of the line are tagged from the LTS branch. The release pipeline refuses a patch tag for
   a branched line that was cut from anywhere else, so a patch can never carry unrelated changes from the
   main branch.
5. A patch release of an older line never moves the `latest` image tags or the `latest` npm tag
   backwards. Its npm packages are published under a tag of their own, such as `lts-1-4`.

## Moving off the line {#moving-off}

An operator on the LTS line does not have to move one minor at a time. The supported route to a newer line
is a direct upgrade that skips the intermediate minors: every migration of every skipped minor runs, in
order, in one `dcctl upgrade`. This is drilled before a newer line is recommended: an instance is seeded
with one of every entity on the LTS release, upgraded straight to the candidate, and every row is read
back unchanged. The same drill is then run against a tree with a deliberately planted migration defect,
and it must fail; a pass that has never been seen to fail is not evidence.

Read the upgrade notes for each release you skip over before you jump, since a deprecation announced in a
skipped minor is announced in that minor's notes.

## What this page does not promise {#out-of-scope}

- Only one LTS line is supported at a time. The release notes announce when a new line is designated and
  the date support for the previous one ends.
- There is no commitment beyond 18 months, and no FIPS-validated build.
- A security fix is a fix to DeviceChain itself and the components it ships. A vulnerability in a service
  you run next to it is outside this page.
