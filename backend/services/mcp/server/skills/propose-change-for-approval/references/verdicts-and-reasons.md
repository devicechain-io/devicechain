# Verdicts, statuses and reasons

## status

| Value | Meaning |
| --- | --- |
| `DRY_RUN` | The request was checked and nothing was stored. |
| `PENDING_APPROVAL` | Stored, waiting for people to approve it. |
| `REFUSED` | Refused. When it was stored for the record, `proposal` is set. |
| `EXISTING` | The same `requestId` was used before for the same request, and the original proposal is returned. |

## verdict

| Value | Meaning |
| --- | --- |
| `APPROVE` | The action is allowed to wait for approval by `requiredApprovals` people, 1 or 2. It has not been approved. |
| `REFUSE` | The action is not allowed. `requiredApprovals` is 0. |

`APPROVE` is the tenant's policy saying the request may go to people. It is not an approval, and you must never describe it as one.

## reasons

`reasons` holds platform-defined codes, for example `CLIENT_NOT_ALLOWED`, `ALARM_NOT_FOUND` or `CLIENT_RATE_EXCEEDED`. Report them exactly as given, and do not work around them:

- a code about the client or the policy means the tenant does not allow this request from you;
- a code about a missing target means a token you sent does not exist for this person: check where you got it;
- a code about a rate or a limit means wait; do not split the request to get under it.

A tool error with a `FORBIDDEN` code means the person you act for, or you, lacks a permission. Report it.

## state of a stored proposal

| Value | Meaning |
| --- | --- |
| `PROPOSED` | Waiting for people. The only state that changes. |
| `EXECUTED` | Approved and carried out. |
| `REJECTED` | A person rejected it. |
| `EXPIRED` | Nobody approved it in time. |
| `REFUSED` | The tenant's policy, or a check made when a person approved it, refused it. The reasons say why. |
| `CANCELLED` | Withdrawn by the agent that proposed it. |
| `KILLED` | Ended because an agent-actions switch was turned off. |

## Command preview

A command dry run returns a `preview` with the device `incarnation`, `impact`, the command `definition` fingerprint, the delivery `cutoff`, and, when a maintenance window holds the command, `dispatchAfter` and `windowToken`. Pass these on to the person as they are.
