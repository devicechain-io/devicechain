---
sidebar_position: 4.5
title: Versioning and Upgrade Policy
---

# Versioning and Upgrade Policy {#versioning-and-upgrade-policy}

From **v1.0.0**, DeviceChain versions follow [semantic versioning](https://semver.org). This page
says what each part of the number promises, which interfaces the promise covers, how you move from one
release to the next, how long a feature is deprecated before it goes, and how a breaking change is announced.

Before v1.0.0 none of this applies. See [Pre-1.0 stability](./releases-and-upgrades.md#pre-10-stability)
for what a pre-1.0 release may change.

## What a version number means {#version-numbers}

A release is one version tag, `vMAJOR.MINOR.PATCH`. The service images, the Helm chart, the operator and
`dcctl` are all published at that same version, so a deployment is one coherent number. See
[Versioning model](./releases-and-upgrades.md#versioning-model).

| Part | What it may contain | What it will not contain |
| --- | --- | --- |
| **Patch** (`1.4.1`) | Bug fixes, security fixes and performance work. | A change to a covered interface, or a removal. |
| **Minor** (`1.5.0`) | New features, new fields and operations, new optional settings, new database migrations, and deprecations. | The removal of anything covered below, or a change in what an existing call means. |
| **Major** (`2.0.0`) | Removals and changes of meaning, including the things a minor deprecated. | Nothing is promised across a major beyond what its release notes and upgrade section say. |

## What the promise covers {#compatibility-surfaces}

The promise is not the same strength for every part of the platform. Some interfaces are what your
devices, your code and your scripts are written against, and those change only in a major release.
Others are tuning and operating surfaces, and those can move in a minor release as long as the
release notes say so.

### Covered: changed incompatibly only in a major release {#covered-interfaces}

| Interface | What is covered |
| --- | --- |
| **Device wire** | The MQTT and HTTP payloads a device sends and receives, the topic and subject layout, and the credential types. A device that works against `1.x` keeps working against every later `1.y`. |
| **GraphQL API** | Every field, argument, type and operation in the served schemas. A minor release adds to the schema; it does not remove or rename anything, change a type, make an optional argument required, or change what an existing field returns. |
| **npm packages** | The public exports of `@devicechain/client`, `@devicechain/dashboards`, `@devicechain/widgets`, `@devicechain/widget-sdk` and `@devicechain/brand`. The widget SDK also carries its own `sdkApiVersion`, which decides whether a stored widget still renders; see [Widget versions and compatibility](../guides/widget-versions-and-compatibility.md). |
| **C# SDK** | The public API of the `DeviceChain.Sdk` NuGet package, which the Unity package builds on. |
| **`dcctl`** | Its commands, subcommands and flags. A flag is not removed or given a different meaning in a minor release. |

Two rules apply to what a minor release may add, so write your clients to tolerate them:

- **New fields and types** appear in GraphQL responses. Ignore the ones you do not know.
- **New values** can appear in an enumeration. Treat an unknown value as "something I do not handle yet",
  not as an error.

### Not covered: described in the release notes {#best-effort-interfaces}

These can change in a minor release. When they do, the release notes and that release's section in
[Releases & Upgrades](./releases-and-upgrades.md) say so, and say what you need to do:

- Helm chart values and the instance configuration document
- Metric names and alert names

## Supported upgrade paths {#upgrade-paths}

- **Within a minor** (`1.4.0` to `1.4.3`) and **to the next minor** (`1.4.x` to `1.5.0`) are the supported paths.
  Run them with `dcctl install` and then `dcctl upgrade`, as described in
  [Rolling upgrades](./releases-and-upgrades.md#rolling-upgrades).
- **Skipping a minor** is not a tested path. To go from `1.4.x` to `1.7.0`, upgrade through `1.5.x`
  and `1.6.x` first. A release's upgrade section says when it requires a particular release first, as the
  [event wire format](./releases-and-upgrades.md#event-wire-format) section does today.
- **Database migrations always upgrade.** From v1.0.0 an instance is never recreated to take a new
  release. A schema change is a migration that is appended to the area's chain, never an edit to an
  earlier one, and every migration can be re-run after a failure. Before v1.0.0 a few releases
  could not be upgraded into at all; that ends with v1.0.0.
- **Upgrades are checked against real data.** Each release's upgrade is exercised by seeding an instance on an
  earlier release, upgrading it, and reading every row back. The release workflow runs this check, and a release whose upgrade fails it is not published.
- **There is no downgrade.** A new release may migrate the database in ways an earlier release cannot read.
  If an upgrade goes wrong, roll forward to a fixed release, or restore from backup using the
  [disaster recovery](./disaster-recovery.md) procedure. Do not use `helm rollback` to go back.

Read the upgrade section for the release you are moving to before you run the upgrade. It lists what
the upgrade checks, what it refuses and anything you must do first.

## Deprecation {#deprecation}

Before something covered above is removed in a major release, a minor release marks it deprecated.

- **A deprecation lasts at least two minor releases** before the removal, and the removal happens only in a major release.
  Something deprecated in `1.5.0` is not removed before `2.0.0`, and not before `1.7.0` has shipped.
- **A deprecated GraphQL field or argument** is marked with the `@deprecated` directive and a reason that names its
  replacement. The reason is visible in the schema, in introspection and in the
  [GraphQL API reference](../reference/graphql-api.md).
- **A deprecated flag, package export or setting** is documented as deprecated, with its replacement,
  in the release notes of the release that deprecates it.
- **A deprecated thing keeps working** exactly as before for the whole window.

A security problem is the one exception. A fix that cannot be made compatibly is released as soon as it
is ready, in the release notes' first lines, with the steps to move to the replacement.

## Support window {#support-window}

Security fixes and bug fixes are released for the **latest minor release** and the **minor release before it**. A
fix is released as a patch of each of them. Releases older than that receive no fix, so keep within one
minor release of the latest. Upgrading to the next minor is the supported path, so staying current is
never more than one step at a time.

## How breaking changes are announced {#announcing-breaking-changes}

- **Release notes.** Every release has hand-written highlights at the top of its notes on the GitHub
  release page. A deprecation, a required step before upgrading, or a refused upgrade is stated there first,
  before the list of changes.
- **The upgrade section.** Each release has a section in [Releases & Upgrades](./releases-and-upgrades.md)
  with the procedure, the checks it makes and anything you must change.
- **The schema.** A deprecated GraphQL field is marked in the served schema, so a tool that reads the schema
  sees it before a person reads the notes.
- **Release candidates.** A release candidate (`vX.Y.Z-rc.N`) is a build you can try an upgrade on before the stable release. It
  never moves the `:latest` image tag.

Because the notes are where a change is announced, read them for each release you install, including
patches.
