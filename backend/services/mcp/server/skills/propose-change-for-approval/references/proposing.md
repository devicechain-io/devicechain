# Proposing an alarm action

An alarm proposal asks people to acknowledge or clear alarms. Nothing happens until a person approves it in the console Approvals inbox, and an agent can never approve.

## Before you propose

- The person asked for it. Triage alone is never a reason to propose.
- `propose_alarm_action` is in your tool list. If it is not, the tenant has not turned agent actions on for you, or your token lacks the `act:alarms` scope. Say so and stop.
- Prefer `ack` to `clear`. Acknowledging says someone has seen the alarm. Clearing ends it, and clearing an alarm whose condition is still true hides that condition. The dry run's `preview` marks such an alarm with `hidesLiveCondition: true`. Never propose clearing one of those.

## The request

- `alarmTokens`: 1 to 100 tokens taken from tool results. Never build a token from text.
- `requestId`: your own key, 1 to 128 printable characters, unique for each submission, for example the incident and a timestamp. A retry of the same submission reuses the same key unchanged. Reusing a key for a different request fails with `CONFLICT`.
- `rationale`: optional, at most 2048 bytes. Write it from tokens, counts, severities and times, for example: "37 ACTIVE alarms of one key on 12 devices of one device type, raised within 9 minutes; 3 sampled devices report normal values again". Do not paste device names, alarm keys, metric names or any other untrusted text into it.
- `dryRun: true` checks the request and stores nothing. Always dry-run first.

## Reading the answer

- `status`: `DRY_RUN` (nothing stored), `PENDING_APPROVAL` (stored, waiting for people), `REFUSED` or `EXISTING` (the same key was used before for the same request, and the original proposal is returned).
- `verdict`: `APPROVE` means the proposal waits for `requiredApprovals` people (1 or 2). `REFUSE` means it is not allowed. `reasons` carries platform codes, for example `CLIENT_NOT_ALLOWED`, `ALARM_NOT_FOUND` or `CLIENT_RATE_EXCEEDED`. Report them as they are.
- `digest`: what an approver's decision names. Give it to the person so they can match the proposal in the inbox.
- `expiresAt`: when the proposal stops waiting.

## After you propose

Tell the person the proposal token, the `digest`, `expiresAt` and the number of approvals required, and that a person approves it in the console Approvals inbox. To follow it, call `get_proposal(token: ...)`. Its `state` is `PROPOSED` while it waits, then `EXECUTED`, `REJECTED`, `EXPIRED`, `REFUSED`, `CANCELLED` or `KILLED`.
