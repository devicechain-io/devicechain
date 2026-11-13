---
sidebar_position: 3.8
title: Reviewing a Rule Change
---

# Reviewing a Rule Change {#reviewing-a-rule-change}

Use the Towerpulse Night Shift episode to compare a rule's published version with two drafts. The first fixes the incident but requests cooling during an ordinary warm-up. The second should pass both windows. See [Automation change review](../concepts/automation-change-review.md) for the model and protection guarantee.

:::note This feature is in preview
The GraphQL types and operations may change in a minor release. Protection is opt-in: no profile is protected after an upgrade, and nothing changes for an unprotected profile.
:::

## Before you start {#before-you-start}

You need a running DeviceChain instance, `dcctl`, and two people with memberships in the tenant that `dcctl sim create` makes for the simulator. The person editing and running needs `device:write` and `event:read` in their own unscoped session. The approver needs those permissions, an unscoped membership and two-factor authentication enrolled. Reading reviews also needs `device:read` and `event:read`.

Use a different person to approve: the runner and draft authors cannot approve by default. For simulator sign-in with a second factor, `dcctl sim` takes `--otp`; see [two-factor authentication with dcctl](../guides/two-factor-authentication.md#dcctl).

## Record the Night Shift episode {#record-night-shift}

Create the simulator tenant and sign-in:

```bash
dcctl sim create nightshift --manifest towerpulse --episode night-shift
```

Run the simulator in its own terminal and keep it running in the foreground:

```bash
dcctl sim run nightshift
```

In another terminal, get the recorded windows:

```bash
dcctl sim windows nightshift
```

The output has two tab-separated lines: role, start and end as RFC3339 UTC. This is the format, with placeholders:

```text
INCIDENT	<start RFC3339>	<end RFC3339>
NORMAL	<start RFC3339>	<end RFC3339>
```

Before the episode starts, the command asks you to run the simulator first. The start instant is recorded when the episode starts. INCIDENT runs from minute 45 to 95 after that instant; NORMAL runs from minute 105 to 160. Wait until about 2 h 42 min after the start so NORMAL ends at least two minutes before review.

The first 45 minutes let the episode settle. During INCIDENT, `tp-vav-tower-f1-south` runs hot and the shipped rule misses it. During NORMAL, `tp-vav-tower-f3-north` has an ordinary short warm-up that must not trigger cooling; `tp-vav-tower-f5-west` hosts a meeting as a control.

The five devices are `tp-vav-tower-f1-north`, `tp-vav-tower-f1-south`, `tp-vav-tower-f3-north`, `tp-vav-tower-f3-south` and `tp-vav-tower-f5-west`. See the command reference for [create](../reference/dcctl.md#dcctl-sim-create), [run](../reference/dcctl.md#dcctl-sim-run) and [windows](../reference/dcctl.md#dcctl-sim-windows).

## Change the rule: Draft A {#draft-a}

Open the detection rules in the draft of profile `tp-vav-profile`. Edit `tp-rule-zone-hot`, “Towerpulse zone hot while occupied”. The published rule checks a five-minute sliding average of `temp_c` greater than 26 while `occupancy > 0`. It raises `zone-hot` with severity `major` and requests `boost-cooling` with payload `{"minutes":15}`.

For Draft A, lower the threshold to 25.5 and keep the five-minute window. Change only this rule.

## Run a review {#run-a-review}

### In the console {#run-in-console}

Open **Change review** → **Run review**. Enter both windows' start and end in local time, converting the UTC times from `dcctl sim windows`. Enter the five device tokens separated by commas or new lines. **Let the server choose the rule** leaves `ruleToken` empty.

Add these two expectations. Choose raises an alarm for INCIDENT and requests no command for NORMAL. The form supports up to ten expectations in four shapes: raises an alarm, raises no alarm, requests a command and requests no command.

```json
{"raised":{"rule":"tp-rule-zone-hot","device":"tp-vav-tower-f1-south","within":"30m"}}
```

```json
{"neverIntent":{"kind":"sendCommand","command":"boost-cooling","device":"tp-vav-tower-f3-north"}}
```

`within` starts at the beginning of its window. Do not use `count` or `allowDegraded`. Choose **Run review**.

### Over GraphQL {#run-over-graphql}

Send operations to `https://<your-host>/api/device-management/graphql` with your tenant access token in `Authorization: Bearer`. This operation takes the input as a variable so you can supply the actual recorded window times and your own `requestId` without copying sample output.

```graphql
mutation RunReview($input: RunAutomationReviewInput!) {
  runAutomationReview(input: $input) {
    token kind state outcome reviewDigest matchesCurrentDraft
    reasons profileChanges { area key change automation }
    fidelity { status reasons label }
    expectations {
      index name window baseline verdict baselineReason reason
      evidence { device at value spanSeconds }
    }
    windowResults {
      role start end leadInStart degraded changesTruncated
      counts { unchanged added removed shifted contentChanged }
      devices {
        device readings { key count } versions { key count }
        stateBase stateDraft divergence lateBase lateDraft
      }
      changes {
        kind device subject baseAt draftAt shiftSeconds contentChanged
        baseContent { severity payload configDigest bodyDigest }
        draftContent { severity payload configDigest bodyDigest }
        afterDivergence
      }
    }
  }
}
```

Set `input.profileToken` to `tp-vav-profile`, `deviceTokens` to the five devices, and `windows` to two objects with `role`, `start` and `end`. Use `INCIDENT` and `NORMAL` and the actual RFC3339 times. Each window must be positive, at most 24 hours, and end at least two minutes before running.

Set `expectations` to two objects with `window` and `expect`: `expect` is the corresponding JSON above encoded as a JSON string, not an object. `name` is optional. Omit `ruleToken` to let the server select it, or use `tp-rule-zone-hot`. `shiftWindowSeconds` is optional and defaults to 15 minutes.

Use a `requestId` of 1–128 printable bytes. Retry the same input with the same ID to retrieve the same review; use a new ID when inputs change.

## Read the result {#read-the-result}

Draft A is expected to be `FAILED`. For INCIDENT, `baseline` fails and `verdict` passes. For NORMAL, the draft fails: it would request `boost-cooling` on `tp-vav-tower-f3-north` during the warm-up. The reason names the command, `{"minutes":15}` and the instant commands diverge. These are expected outcomes, not sample live output.

Read `expectations` from active (`baseline`) to draft (`verdict`), then the reasons and evidence. In `windowResults`, compare `ADDED`, `REMOVED`, `SHIFTED` and `CONTENT_CHANGED` per device. Counts are complete even when `changesTruncated` says the list exceeds 2,000 changes.

Check metric and profile-version coverage, `stateBase`, `stateDraft`, `lateBase`, `lateDraft` and `divergence`. Read `fidelity` and `degraded` before trusting a quiet result. Unsupported rules, incomplete history, caps, time budgets, corrupt records, evaluation errors, declined late readings, missing metric readings, unknown starting state, an already-raised alarm for a “never” expectation, or evidence after command divergence can make a review `INCONCLUSIVE`. It cannot be approved. See [why inconclusive is strict](../concepts/automation-change-review.md#why-inconclusive-is-strict).

## Try Draft B {#draft-b}

Keep the threshold at 25.5 and increase the sliding window to 15 minutes. Run a new review over the same devices, windows and expectations with a new `requestId`. Draft B is expected to be `PASSED`: it catches the hot zone without requesting cooling during the ordinary warm-up.

Check the actual evidence. Passing requires every draft expectation to pass, at least one expectation in each window, and a complete comparison with neither window degraded. Editing the draft after a run makes that review stale; `matchesCurrentDraft` and the console banner show this.

## Approve the review {#approve-the-review}

Choose **Copy link** and give it to the second person. The page is `/automation-reviews/<token>`. They open it in their own signed-in session, read the evidence and choose **Approve**. They must be different from the person who ran the review and everyone who edited the draft, unless policy allows self-approval.

The approver needs `device:write`, `event:read`, unscoped membership and a recently verified second factor: within 15 minutes by default. The console offers step-up. For API clients, use the tenant API's `stepUp(refreshToken, code)` described in the [GraphQL reference](../reference/graphql-api.md).

```graphql
mutation ApproveReview($token: String!, $digest: String!) {
  approveAutomationReview(token: $token, digest: $digest) {
    token state requiredApprovals approvals { principal approvedAt policyRevision }
    approvedAt expiresAt
  }
}
```

Supply the review's `token` and the exact `reviewDigest` the person inspected. Only `RAN` reviews with `PASSED` or `NO_AUTOMATION_CHANGE` can be approved. If two approvals are required, two different qualifying people must approve. **Reject** rejects a pending or approved review, with an optional note and no step-up.

Approval lasts 24 hours by default. Check `expiresAt`; an expired approved review keeps state `APPROVED`. Signing out does not end approval, but every approver's enabled membership and permissions are checked again at publish.

## Protect the profile {#protect-the-profile}

In **Change review** → **Publish protection** → **Edit protection**, enable **Require an approved review to publish**. Set **Required approvals**, **Allow self-approval**, **Approval lifetime (hours)** and **Second-factor maximum age (minutes)**, then choose **Save protection**. Defaults are one approval, no self-approval, 24 hours and 15 minutes. Protection requires a published profile and eligible enabled active rules.

You can also use the API:

```graphql
mutation ProtectProfile($profileToken: String!, $settings: AutomationReviewPolicyInput!) {
  setAutomationReviewPolicy(profileToken: $profileToken, settings: $settings) {
    applied
    policy {
      effective { required requiredApprovals allowSelfApproval approvalTtlSeconds stepUpMaxAgeSeconds }
      revision pendingChange { token effectiveAt }
    }
  }
}
```

Use `required: true`. `requiredApprovals` is 1 or 2, `approvalTtlSeconds` is 900–604800, and `stepUpMaxAgeSeconds` is 60–3600. Use your own unscoped session with `device:write`.

Tightening applies immediately. Loosening waits for a second person's approval with a recent second factor; the proposer cannot approve. If the current policy allows self-approval, loosening also takes effect on its own after 24 hours, applied by the next protected operation, unless a second person approves it sooner. Use **Cancel policy change** to cancel it. The profile remains protected while pending. If tightening means existing approvals no longer satisfy policy, obtain a new review and approvals before publishing.

## Publish with the review {#publish-with-the-review}

Open **Versions**, select the approved review of the current draft in **Approved review**, then publish. The publisher needs `device:write` and need not be an approver.

```graphql
mutation PublishReviewedProfile($token: String!, $automationReviewToken: String!) {
  publishDeviceProfile(token: $token, automationReviewToken: $automationReviewToken) {
    version automationReviewToken
    automationReview { token approvals { principal } }
  }
}
```

Use the profile token and the approved review token. Publication consumes that review exactly once. Changed draft, baseline, expired evidence or approvals that no longer satisfy current policy prevent publishing. On an unprotected profile `automationReviewToken` is ignored.

## Trace a version to its review {#trace-a-version}

`DeviceProfileVersion.automationReviewToken` identifies the consumed review. `DeviceProfileVersion.automationReview` holds the approvals, expectations, `inputDigest` and result. The review's `consumedVersion` points back to the published version.

```graphql
query ReadReview($token: String!) {
  automationReview(token: $token) {
    token profileToken baselineVersion state outcome consumedVersion
    reviewDigest inputDigest resultDigest
    approvals { principal approvedAt policyRevision }
    expectationInputs { window name expect }
    result
  }
}
```

Read this with `device:read` and `event:read`, unscoped. Runs, approvals and rejections are also in the audit journal.

## Break glass {#break-glass}

To roll back a protected profile, give a non-empty reason of at most 512 characters from your own session with `device:write` and a second factor verified within the profile's step-up age.

```graphql
mutation RollBackProtectedProfile($token: String!, $version: Int!, $reason: String!) {
  rollbackDeviceProfile(token: $token, version: $version, breakGlassReason: $reason) {
    token
  }
}
```

In the console, **Change a protected profile** asks for the reason and then a second factor if needed. The same requirements apply to re-pointing a device type to or from a protected profile (`updateDeviceType`) and moving a device out of a type bound to it (`updateDevice`). Each takes `breakGlassReason`.

Read **Break-glass record**, or `automationReviewBreakGlass(profileToken)` with `device:read`, unscoped. It records action, target, reason, actor and time. Rollback makes outstanding approvals fail with `REVIEW_STALE_BASELINE_CHANGED`; run and approve a new review. Rolling back to ineligible rules is refused.

## What can go wrong {#what-can-go-wrong}

Run-time refusals store no review. An `INCONCLUSIVE` review is stored evidence that could not prove the change safe.

| Refusal | What to do |
| --- | --- |
| `REVIEW_REFUSED` | Read the refusal and correct the inputs, such as an invalid rule. |
| `REVIEW_UNAVAILABLE` | Restore the comparison service or complete the upgrade; protected publishing also needs approver standing to be checked. |
| `NO_BASELINE`, `PROFILE_NOT_PUBLISHED` | Publish the profile before reviewing or enabling protection. |
| `RATE_LIMITED` | Wait until fewer than 60 reviews fall within the past hour. |
| `CONFLICT` | Use a new `requestId` for different inputs. |
| `NOT_APPROVABLE`, `REVIEW_NOT_PENDING` | Check outcome and state; only eligible `RAN` reviews can be approved. |
| `SECOND_FACTOR_REQUIRED`, `STEP_UP_REQUIRED` | Use your own session with a second factor, or complete step-up. |
| `APPROVER_IS_AN_AGENT`, `SERVICE_TOKEN_REFUSED`, `NOT_A_PERSON`, `SCOPED_GRANT_NOT_SUPPORTED` | Use a person's own unscoped tenant session. |
| `SELF_APPROVAL_REFUSED` | Ask a different qualifying person. A policy-change proposer can never approve their own change. |
| `CONCURRENT_APPROVAL` | Reload after the other approval. |
| `REVIEW_STALE_DRAFT_CHANGED`, `REVIEW_STALE_BASELINE_CHANGED` | Run and approve a new review of the current draft and baseline. |
| `DIGEST_MISMATCH` | Reload and check the exact evidence and digest; stored evidence must verify. |
| `REVIEW_REQUIRED`, `REVIEW_NOT_FOUND`, `REVIEW_WRONG_PROFILE` | Select an approved review for this profile's current draft. |
| `REVIEW_NOT_APPROVED`, `REVIEW_ALREADY_USED`, `REVIEW_EXPIRED` | Obtain a current, unused approved review. |
| `REVIEW_STALE_POLICY_CHANGED` | Obtain approvals that satisfy the current protection policy. |
| `APPROVER_NO_LONGER_QUALIFIES` | Obtain approval from people who still qualify at publish. |
| `PROTECTION_INELIGIBLE` | Read the reasons and remove ineligible rule dependencies. |
| `INVALID_POLICY` | Use the supported approval count, lifetime and step-up ranges. |
| `BREAK_GLASS_REASON_REQUIRED` | Supply a non-empty reason, at most 512 characters. |
| `PROFILE_PROTECTED` | Remove protection before deleting; loosening waits for approval or the delay. |

## Related pages {#related-pages}

- [Automation change review](../concepts/automation-change-review.md)
- [Testing your rules](../guides/testing-your-rules.md)
- [Two-factor authentication](../guides/two-factor-authentication.md)
- [GraphQL API](../reference/graphql-api.md)
