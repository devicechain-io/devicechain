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
| **Instance API (from v2.0)** | `core.devicechain.io/v1 Instance`: spec and status fields, condition types and reasons, and the documented annotations and finalizer. Coverage starts at v2.0; v1 is currently Preview, as described below. |

Two rules apply to what a minor release may add, so write your clients to tolerate them:

- **New fields and types** appear in GraphQL responses. Ignore the ones you do not know.
- **New values** can appear in an enumeration. Treat an unknown value as "something I do not handle yet",
  not as an error.

### Not covered: described in the release notes {#best-effort-interfaces}

These can change in a minor release. When they do, the release notes and that release's section in
[Releases & Upgrades](./releases-and-upgrades.md) say so, and say what you need to do:

- Helm chart values and the instance configuration document
- Metric names and alert names
- The MCP tool catalog (tool names, inputs and result shapes)

### Preview: may change in a minor release, for a limited time {#preview-interfaces}

A new served surface can spend its first minor release in a **preview** tier before it is covered by
the promise above. A surface in the preview tier says so where you read about it, in its reference or
guide page and, for each GraphQL operation, field, argument, type and enum value in that surface,
in its description. While a surface is in preview it can change
incompatibly in a minor release; when it does, that release's section in
[Releases & Upgrades](./releases-and-upgrades.md) says what changed and what to do. A surface stays in
preview until its own stated promotion condition is met, then becomes covered. The **default**
condition is two minors after it joined the preview tier; a surface may state a different condition
below. Each listed surface states which condition applies. An incompatible change to a preview surface is not exempt from the project's
compatibility checks: it is declared there as an intended break, the same way as any other, so no
preview change ships unnoticed. A file format in the preview tier carries a version, such as
`v1alpha1`, and a tool that does not know the version refuses the file rather than guessing.

#### Instance API compatibility {#instance-api-compatibility}

**Instance v1 is Preview in v1.10; it becomes covered at v2.0.** This commitment is prospective.
The existing status promise continues: all status fields are optional, changes are additive only,
and readers treat unknown condition types and reasons as Unknown.
Instance `v1beta1` remains served throughout v2.x and is to be deprecated at v2.0.
`spec.grafanaSSO`, `spec.compact` and the `InstanceConfiguration` kind are not removed at v2.0.
They remain available while deprecated; removal is a v3 project and must satisfy the two-minor
deprecation window in [Deprecation](#deprecation). Storage is still `v1beta1`; storage migration has not shipped.

#### Executor API promotion {#executor-api-promotion}

**`ExecutorEnrollment` and `InstanceOperation` remain `core.devicechain.io/v1beta1` through v2.0.**
They are an explicit exception to the two-minor Preview limit: promotion is triggered
when action execution is enabled in a released build, after the checks that refuse unsafe instance actions have shipped and
explicit provider privileges are in place. Their served interfaces are covered from that release;
any later API-version promotion must preserve existing persisted objects. Instance v1 promotion
alone does not promote the executor kinds. No executor action is executable in the current build:
otherwise-valid requests are refused as `NotExecutableInThisBuild` (see
[In-cluster executor](./kubernetes-operator.md#in-cluster-executor)).

Rule testing and product as code are **covered from v1.10** and are no longer Preview from that release.
Rule testing includes the `evaluateRules` operation of the
event-processing API, the scenario and test file formats, and the `dcctl test` command that reads them
(see [Testing your rules](../guides/testing-your-rules.md)).
Product as code includes `dcctl product export`,
`plan` and `apply`, and their bundle format (see [Product as Code](../guides/product-as-code.md));
both surfaces joined Preview in v1.3.

Other Preview surfaces include agent actions: the `act:*` OAuth scopes and their derived proposal authorities; the agent policy
operations (`agentPolicy`, `agentPolicyVersions`, `saveAgentPolicyDraft`, `publishAgentPolicy`,
`rollbackAgentPolicy`, `setAgentActuation`) and its document (format `v1alpha1`); the
`agentActuationDecision` and `agentExecutionDecision` queries; the operator's per-tenant controls
(`tenantAgentActuation`, `setTenantAgentActuationSuspended`); agent service-account creation
(`ServiceAccountCreateRequest.agent`) and the agent-specific token authorities described by
`ServiceAccount.roles`; and the agent proposal operations (`proposeAlarmAction`, `approveAgentProposal`,
`rejectAgentProposal`, `agentProposals`, `agentProposal`) and command-delivery's `proposeCommand`,
`approveAgentProposal`, `rejectAgentProposal`, `agentProposals` and `agentProposal`
(see [Agent actions](../concepts/mcp.md#agent-actions)); promotion: **covered from v2.0, when approvals move to a common approval model**.
When enabled, MCP serves the Preview proposal tools `propose_alarm_action` and `propose_command`
(which calls `proposeCommand`), plus `get_proposal` and `list_my_proposals` for reading proposals.
Their promotion condition is **covered from v2.0, when approvals move to a common approval model**. People approve in the
console; MCP offers no approval, rejection or execution tool. The ordinary MCP read tools remain
in the not-covered catalog above.

Automation review: device-management's `automationReviewPolicy`, `automationReviewBreakGlass`,
`automationReview`, `automationReviews`, `setAutomationReviewPolicy`,
`approveAutomationReviewPolicyChange`, `cancelAutomationReviewPolicyChange`, `runAutomationReview`,
`approveAutomationReview` and `rejectAutomationReview`, and event-processing's `compareRuleVersions`
(service-identity only; see [Automation Change Review](../concepts/automation-change-review.md));
promotion: **covered from v2.0, when approvals move to a common approval model**.
The GraphQL preview surfaces include their dedicated input and result types, fields, arguments and
enum values, under the same promotion condition as their operations. The `ServiceAccount.agent`
field is not in preview: it is covered from the release that adds it. Ordinary service-account roles remain covered.

Bulk change readiness joined the preview tier in **v1.9**, when its preview clock started.
In **v1.10** the device-side surface is called bulk changes: the `bulkChangeReadiness` query,
`BulkChangeReadiness` and `BulkChangeEnforcement`, including their fields and enum values,
and `dcctl bulk-change readiness`. The deprecated `fleetOperationReadiness`,
`FleetOperationReadiness`, `FleetOperationEnforcement` and `dcctl fleet-op readiness` keep
working for compatibility, including v1.9 clients. The rename does not restart the preview
clock. Promotion: **two minors after it joined (default)**. They require `fleet:operate`; scoped members are refused.

## Supported upgrade paths {#upgrade-paths}

- **Within a minor** (`1.4.0` to `1.4.3`) and **to the next minor** (`1.4.x` to `1.5.0`) are the supported paths.
  Run them with `dcctl install` and then `dcctl upgrade`, as described in
  [Rolling upgrades](./releases-and-upgrades.md#rolling-upgrades).
- **Skipping a minor** is not a tested path. To go from `1.4.x` to `1.7.0`, upgrade through `1.5.x`
  and `1.6.x` first. A release's upgrade section says when it requires a particular release first, as the
  [event wire format](./releases-and-upgrades.md#event-wire-format) section does today.
  `dcctl upgrade` enforces this: it refuses a move more than one minor ahead of the version the
  instance's workloads run (read from the running images, not from the declared version), and
  across a major allows only the major's first minor (`x.0`). The `--allow-skipped-minor` flag
  overrides the refusal; that path is unsupported and untested, and the flag never disables a
  protection the database enforces.
- **Database migrations always upgrade.** From v1.0.0 an instance is never recreated to take a new
  release. A schema change is a migration that is appended to the area's chain, never an edit to an
  earlier one, and every migration can be re-run after a failure. Before v1.0.0 a few releases
  could not be upgraded into at all; that ends with v1.0.0.
- **Upgrades are checked against real data.** Each release's upgrade is exercised by seeding an instance on an
  earlier release, upgrading it, and reading every row back. The release workflow runs this check from the
  previous stable release. If it fails, the release is stopped: no GitHub release, `dcctl` binaries or SDK packages
  are published for it. Its container images and chart may already be in the registry, but they are not a release.
- **There is no downgrade.** A new release may migrate the database in ways an earlier release cannot read.
  dcctl enforces this: `dcctl upgrade`, and a `dcctl bootstrap` re-run over an existing instance, refuse a
  release older than the one the instance is declared at,
  and `dcctl install` refuses to run with a dcctl older than the one that last installed the cluster.
  A release tag is a release on any registry, so this holds for an instance pulling from a mirror too.
  Neither check has an override. A dcctl older than this policy does not carry these checks, so use
  the current release's dcctl.
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

| Release | What it receives |
| --- | --- |
| The **latest minor release** | Bug fixes, and security fixes of every severity |
| The **minor release before it** | Security fixes rated High or Critical, for 90 days after the next minor release ships |
| Anything older | Nothing. Upgrade to a supported release |

Fixes ship as patch releases of each supported minor. Keep within one minor release of the latest; upgrading to
the next minor is the supported path, so staying current is never more than one step at a time. The same window
is stated in the [security policy](https://github.com/devicechain-io/devicechain/blob/main/SECURITY.md), with how
to report a vulnerability and how quickly a fix is targeted.

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
