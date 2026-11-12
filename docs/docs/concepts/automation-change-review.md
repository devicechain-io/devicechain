---
sidebar_position: 4.1
title: Automation Change Review
---

# Automation Change Review {#automation-change-review}

An automation change review compares a published detection rule with its draft over recorded device events. You choose an incident the change should fix and a period of ordinary operation it must not disturb. A protected profile requires an approved review before publishing. Follow [Reviewing a rule change](../guides/reviewing-a-rule-change.md) to try it.

:::note This feature is in preview
The GraphQL types and operations may change in a minor release. Protection is opt-in: no profile is protected after an upgrade, and nothing changes for an unprotected profile.
:::

## What a review compares {#what-a-review-compares}

A review compares the active (published) version and the draft of one detection rule on one device profile, for an explicit set of devices. You supply exactly one INCIDENT window and one NORMAL window.

The platform reads each window's recorded resolved events once, including events from every profile version recorded in that window. Its own detection evaluator runs twice on that same list, once per rule version. Both sides receive identical inputs.

Nothing is executed: no alarm is raised, no command is sent, and no webhook or message leaves the platform. The review records alarm transitions and command or connector requests as **intents**: what the rule would ask for.

Each window lists changes per device:

| Kind | Meaning |
| --- | --- |
| `ADDED` | Only the draft does it. |
| `REMOVED` | Only the active rule does it. |
| `SHIFTED` | Both do it, at different times. Pairing tolerance defaults to 15 minutes; use `shiftWindowSeconds` to change it. |
| `CONTENT_CHANGED` | Both do it, but command payload, alarm severity, or connector request body/config digest differs. |

Counts (`unchanged`, `added`, `removed`, `shifted`, `contentChanged`) are complete before truncation. The list holds at most 2,000 changes per window; `changesTruncated` says when more exist.

Coverage reports readings per metric and profile version, each side's state at window start (`UNKNOWN`, `CLEAR`, `RAISED`), when commands first diverge, and readings declined as late on each side.

Before each window, the engine reads a lead-in to establish state: the rule's horizon plus five minutes, capped at six hours. The horizon is 30 minutes for a threshold rule, its hold time for a duration rule, and its window for an aggregate rule.

## Kinds of change {#kinds-of-change}

The platform classifies the draft against the active version.

| `kind` | What changed |
| --- | --- |
| `RULE_CHANGE` | Exactly one rule's definition, enabled flag or group scope changed, and nothing a rule reads or sends changed. This rule is replayed. |
| `NO_AUTOMATION_CHANGE` | No rule changed and nothing a rule reads or sends changed. No replay runs; the outcome is `NO_AUTOMATION_CHANGE` and can be approved like `PASSED`. Examples include a description-only edit or a decoder edit when no rule is enabled. |
| `UNREVIEWABLE` | Two or more rules changed, or a metric or command a rule reads or sends changed. The outcome is `INCONCLUSIVE`; `reasons` names each change. Split the work into one rule per review. |

While any rule in either version is enabled, adding or removing any metric, changing anything other than its labels, or changing the decoder also counts as an automation change, even if no rule names that metric. A measurement event is validated as a whole, and the decoder produces the values rules read.

Other edits appear in `profileChanges` with `automation: false`. Approving acknowledges those edits too. The server finds the changed rule when `ruleToken` is omitted; if supplied, it must name that rule.

## Outcomes {#outcomes}

`outcome` describes the evidence; `state` describes the review's lifecycle.

| Outcome | Meaning |
| --- | --- |
| `PASSED` | Every draft expectation is `PASS`, both windows have at least one expectation, neither window is degraded, and nothing makes the comparison incomplete. No expectations can never produce `PASSED`. |
| `FAILED` | At least one expectation is `FAIL` and none is `INCONCLUSIVE`. |
| `INCONCLUSIVE` | Everything else. It cannot be approved. |
| `NO_AUTOMATION_CHANGE` | There is no automation change to replay. It can be approved. |

### Why inconclusive is strict {#why-inconclusive-is-strict}

A review that cannot prove the draft is safe never passes. Check these causes:

- Either rule cannot be reproduced from recorded telemetry: `fidelity.status` is `UNSUPPORTED`, with reasons, and no diff is computed.
- A window is degraded: its lead-in or start has aged out of stream retention, a read or scan cap or time budget was reached, records are corrupt, or evaluation failed. See `degraded`.
- A reviewed device has late readings that were declined, no readings for a metric the rule reads, or `UNKNOWN` state at window start because the lead-in has no evidence.
- A “never” expectation begins with its alarm already raised.
- Evidence falls after the two sides' commands diverge. The draft's command would change what the device did next, so later recorded readings no longer describe the draft's world.

`fidelity.label` reads “replay of recorded inputs”.

## What can be replayed {#what-can-be-replayed}

Threshold, duration, and aggregate rules with tumbling or sliding windows can be replayed. Supported actions are raise alarm, send command, HTTP call and publish to a connector.

The following produce `fidelity.status: UNSUPPORTED`, outcome `INCONCLUSIVE`, and no diff: delta-rate, repeating, absence, correlation and connectivity rules; aggregate rules with session or count windows; rules reading device attributes or geofence position; relationship-anchor correlation; group-scoped rules; calculated metrics whose formulas read device constants; and any other action type.

## Expectations {#expectations}

Use up to ten expectations in the vocabulary of [rule tests](../guides/testing-your-rules.md). Assign each to INCIDENT or NORMAL. The console offers four shapes: raises an alarm (`raised`), raises no alarm (`neverRaised`), requests a command (`intent`, kind `sendCommand`), and requests no command (`neverIntent`, kind `sendCommand`).

Over GraphQL, `expect` is a JSON string, at most 8,192 bytes. `within` counts from the window's start. A review expectation cannot use `count` or `allowDegraded`, and any named device must be reviewed.

Each expectation has an active-rule verdict (`baseline`) and a draft verdict (`verdict`): `PASS`, `FAIL` or `INCONCLUSIVE`. Read `baselineReason`, `reason` and `evidence` for the explanation and readings behind them.

## Approvals {#approvals}

Only a `RAN` review with outcome `PASSED` or `NO_AUTOMATION_CHANGE` can be approved. `approveAutomationReview(token, digest)` takes the `reviewDigest` shown to the approver, so the person approves exactly the stored evidence.

Use your own signed-in, unscoped tenant session with `device:write` and `event:read`. Agent tokens, service tokens, service accounts and members scoped to part of the tenant are refused. Your second factor must have been verified within the profile's step-up age, 15 minutes by default. The console prompts for step-up; API clients use [in-session step-up in the GraphQL reference](../reference/graphql-api.md).

By default, the person who ran the review and every draft author cannot approve it. Draft authors are people who edited the profile draft, including metrics, commands and rules, since the baseline was published, as read from the audit journal. The platform excludes a person when in doubt. A policy can allow self-approval.

The default is one approval; policy can require two different people. Without self-approval, neither may be the creator or a draft author. Approval records include the person, authentication methods, second-factor time, approval time and policy revision.

Approval is judged from the approver's own session token at approval time. Signing out later does not end it. At publish, every approver must still have enabled, unscoped membership with `device:write` and `event:read`, or publishing is refused with `APPROVER_NO_LONGER_QUALIFIES`.

The lifecycle is `RAN` → `APPROVED` when enough people approve → `CONSUMED` when published. `consumedVersion` identifies the version. A `RAN` or `APPROVED` review can be rejected with `rejectAutomationReview(token, note)`, without step-up. Runs, approvals and rejections are journaled with the person as actor.

Approval lifetime defaults to 24 hours. An expired approval stays in state `APPROVED`; check `expiresAt`. Publishing applies both the stored expiry and the current policy's lifetime, and refuses expired evidence with `REVIEW_EXPIRED`.

Approval also checks that draft, baseline and stored evidence still match. `matchesCurrentDraft` shows whether the draft is still the reviewed one; the console displays a banner after it changes. Share `/automation-reviews/<token>` with anyone permitted to read reviews.

## Protected profiles {#protected-profiles}

Protection requires a published profile. On a protected profile, `publishDeviceProfile` must supply `automationReviewToken` for an approved review of exactly the current draft. The publisher needs `device:write` and need not be an approver. One review is consumed by exactly one publish. On an unprotected profile the token is ignored and publishing works as before.

Read `automationReviewPolicy(profileToken)` for `effective` settings, `revision` and `pendingChange`. Use `setAutomationReviewPolicy(profileToken, settings)` to change `required`, `requiredApprovals`, `allowSelfApproval`, `approvalTtlSeconds` and `stepUpMaxAgeSeconds`; it returns `applied` and `policy`.

Tightening protection applies at once: turn it on, require more approvals, disallow self-approval, or shorten the lifetime or step-up age. Loosening waits as `policy.pendingChange` with `applied: false`. A second person approves it through `approveAutomationReviewPolicyChange(token)` using their own unscoped session, `device:write` and a recent second factor. The proposer can never approve it.

If the current policy allows self-approval, loosening instead takes effect 24 hours later at `effectiveAt`, applied by the next protected operation. Either path can be cancelled with `cancelAutomationReviewPolicyChange(token)`. The profile stays protected while the change is pending.

The console's **Change review** tab has **Publish protection**, **Edit protection**, pending-change approval and **Cancel policy change**, and the **Break-glass record**. The **Versions** tab offers an **Approved review** picker for the current draft.

### The guarantee {#the-guarantee}

On a protected profile, the rules devices execute and the device types bound to the profile change only through a reviewed and approved publish, or through a recorded break-glass action. This covers publishing, rollback, re-pointing a device type to or from the profile, moving a device out of a type bound to it, and deleting the profile.

Published versions of a protected profile cannot be changed. They are deleted only together with their profile; no operation deletes a version on its own. Deleting a protected profile is refused, so its version history stays while protection is on.

The database enforces this for every client, including previous-release service pods during a rolling upgrade. Statements that would change what devices run on a protected profile are refused rather than applied.

### What protection does not cover {#what-protection-does-not-cover}

Protection does not cover creating, enabling, disabling or deleting devices; adding devices to a bound type; creating a new type bound to the profile or deleting an empty one; device attributes and metadata; or entity-group membership. This is why protected rules cannot depend on attributes, constants, groups, anchors or geofences.

## Eligibility {#eligibility}

To enable protection, every enabled rule in the active version must be eligible. A draft published under protection must also pass the eligibility check. No enabled rule may read device attributes, read a calculated metric whose formula reads device constants, be group-scoped, correlate devices through relationship anchors, or read geofence position.

Those inputs can change rule behaviour without publishing, so recorded telemetry cannot support the guarantee. The check is conservative: mentioning such an input, or a rule the check cannot read, counts as using it. Ineligible protection is refused with `PROTECTION_INELIGIBLE` and reasons.

## Break-glass {#break-glass}

Three operations require `breakGlassReason` on a protected profile: `rollbackDeviceProfile`, `updateDeviceType` when re-pointing a type to or from the profile, and `updateDevice` when moving a device out of a type bound to it.

Use a person's own session with `device:write`, a non-empty reason of at most 512 characters, and a second factor verified within the profile's step-up age. The console retries through **Change a protected profile**, asking for the reason and then a second factor if needed.

The platform records action (`ROLLBACK`, `REPOINT_TYPE`, `MOVE_DEVICE`), target, reason, actor and time in the same transaction, and journals it. Read `automationReviewBreakGlass(profileToken)` for the newest 200 records at most, or open **Break-glass record**.

A rollback moves the baseline. Outstanding approvals then fail with `REVIEW_STALE_BASELINE_CHANGED`; run and approve a new review. A break-glass rollback to a version with ineligible rules is refused.

## Deleting a profile {#deleting-a-profile}

Deleting a protected profile is refused with `PROFILE_PROTECTED`. Remove protection first; that loosening waits for a second person or the delay. Deleting an unprotected profile removes its policy, pending policy changes and unconsumed reviews, while keeping consumed reviews and break-glass records as history.

## Limits {#limits}

| Item | Limit or default |
| --- | --- |
| Devices | 1–50 distinct devices. |
| Windows | Exactly one INCIDENT and one NORMAL; each positive, at most 24 hours, ending at least two minutes before the run. |
| Automatic lead-in | At most six hours. |
| Expectations | At most 10; each at most 8,192 bytes. |
| Listed changes | At most 2,000 per window; counts are complete. |
| Runs | At most 60 per tenant over the past hour. |
| `requestId` | 1–128 printable bytes. Repeating identical inputs returns the same review without counting another run; different inputs with the same ID give `CONFLICT`. |
| Required approvals | 1 by default; 1 or 2. |
| Self-approval | Disabled by default. |
| Approval lifetime | 86,400 seconds by default; 900–604,800 seconds (15 minutes–7 days). |
| Step-up age | 900 seconds by default; 60–3,600 seconds. |
| Rejection note | At most 512 bytes. |
| Break-glass reason | Non-empty; at most 512 characters. |

## Required permissions {#required-permissions}

An unscoped session is a membership not limited to part of the tenant.

| Action | Requirements |
| --- | --- |
| Run | `device:write` + `event:read`; person's own unscoped session. |
| Read (`automationReview`, `automationReviews`, share link) | `device:read` + `event:read`, unscoped. Reviews contain device readings. |
| Approve | `device:write` + `event:read`; own unscoped session, recent second factor, not creator or draft author unless policy allows self-approval. |
| Reject | `device:write` + `event:read`; own unscoped session. |
| Read protection or break-glass record | `device:read`, unscoped. |
| Change protection or cancel pending change | `device:write`; own unscoped session. |
| Approve pending loosening | `device:write`; own unscoped session, recent second factor, not proposer. |
| Publish protected profile | `device:write` and an approved review. |
| Break-glass | `device:write`; own session, reason, recent second factor. |

## Upgrading {#upgrading}

During a rolling upgrade, roll user-management before device-management: protected publishing returns `REVIEW_UNAVAILABLE` while user-management is older. Turn protection on only after rollout. If event-processing is older than device-management, running a review returns `REVIEW_UNAVAILABLE` until event-processing is rolled.

## Related pages {#related-pages}

- [Reviewing a rule change](../guides/reviewing-a-rule-change.md)
- [Testing your rules](../guides/testing-your-rules.md)
- [Event processing](../concepts/event-processing.md)
- [Two-factor authentication](../guides/two-factor-authentication.md)
- [GraphQL API](../reference/graphql-api.md)
