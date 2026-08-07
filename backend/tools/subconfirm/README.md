# subconfirm — a subscription the broker never confirmed

A `go/analysis` pass, run over every workspace module by
`hack/check-subscribe-confirmed.sh` and gated by the `subscriptions` job in CI.

It reports two defects that are silent by construction:

1. **`nats.Conn.Subscribe` and friends are asynchronous.** They append a SUB to the
   connection's write buffer and return; until the server reads it, the subscription
   does not exist. Core NATS *drops* a publish with no subscriber rather than queueing
   it, so the message is not late, it is gone. A component can subscribe, report itself
   started, and be handed nothing.
2. **paho's `mqtt.Client.Subscribe` is acknowledged, but paho never reports a
   refusal.** Its SUBACK handler copies the broker's return codes into the token and
   completes it without calling `setError`, so a broker answering `0x80` yields a token
   that waits successfully with `Error() == nil`. paho's own `WaitTokenTimeout` shares
   the blind spot.

Both were live. The auth callout shipped with (1) and refused real devices with a bare
`EOF` while logging that devices were authenticated; the DETECT tenant-purge responder
shipped with it too, where the window writes a *false clean* into a deletion ledger
nothing re-checks. (2) was written twice.

## Why it is an analyzer and not a grep

This is the whole reason the tool exists. `.Subscribe(` matches at least four unrelated
APIs in this repo:

| receiver | reported? | why |
| --- | --- | --- |
| `*nats.Conn` | ✅ | asynchronous; the server may not have registered it |
| `mqtt.Client` (paho) | ✅ | the SUBACK's refusal code is never surfaced as an error |
| `nats.JetStreamContext` | ❌ | creates its consumer with a request to the JetStream API, so it is confirmed by construction |
| `graphqlws.Client`, `graphql.Schema` | ❌ | not a broker at all |

The first inventory of these sites was done textually and was wrong in **both**
directions — it counted a JetStream subscribe and a line inside a comment as defects,
and it had no way to see that the receiver's type was the only thing that mattered. A
grep narrowed with `nc.` or `conn.` is a guess about variable names. Only a type checker
can tell them apart, which is also why the useful half is what the pass stays *quiet*
about.

## What satisfies it

For NATS, either wrapper — `messaging.SubscribeSynced` / `QueueSubscribeSynced` — or a
`messaging.ConfirmSubscribed` (or a raw `Flush`) anywhere later in the same function,
since one round trip confirms every subscription issued on the connection so far.

For MQTT, `messaging.SubscribeMqttConfirmed`, which reads the granted QoS. There is no
after-the-fact equivalent: the answer arrives with the subscribe and is gone if nobody
reads it. A QoS *downgrade* is legal and is not a failure.

## Suppressing a report

Some unconfirmed subscribes are correct. The one that recurs is a **same-connection**
subscribe followed by the publish it is waiting for: both writes land in one buffer
under one lock, so the server reads SUB before PUB and a flush buys nothing but a round
trip. Say so, with a reason:

```go
//subconfirm:ok the request below goes out on this same connection, so SUB is
// ordered ahead of it
sub, err := nc.SubscribeSync(inbox)
```

The directive is accepted as a trailing comment on the call, or anywhere in the comment
block that ends immediately above the enclosing statement — so a reason too long for one
line runs on underneath it. It suppresses **one statement**, never a function: a
directive that covered a whole function would silently extend to the next subscribe
somebody adds to it.

A reason is required, and a directive that no longer suppresses anything is itself
reported. A suppression that outlives its subject is how a guard quietly stops guarding.

## Known limits

Written down rather than left to be discovered, because a guard that looks thorough is
worse than one whose edges are stated. None of these has an instance in the tree today;
all were found by attacking the pass rather than by it failing.

**It matches a declared type, so a type it cannot name escapes.** A method *value*
(`f := nc.Subscribe; f(...)`) resolves to a variable, not a method, and is not reported.
Neither is a call through a hand-declared local interface that *redeclares* the
signature — a test seam is the plausible way that appears. An interface that **embeds**
`nats.Conn` or `mqtt.Client`, and a struct that embeds `*nats.Conn` (promoted method),
both **are** reported.

**It reads position, not execution order.** `defer nc.Subscribe(...)` followed by a
flush looks confirmed and is not; a deferred flush looks unconfirmed and is not. Both
are contrived, and the second errs toward reporting.

**Confirmation must be in the same function.** A helper that subscribes and returns,
with the caller doing one `ConfirmSubscribed`, is reported — as is a subscribe followed
by a `SubscribeSynced` on the same connection, whose internal flush genuinely does
confirm the earlier one. Both are correct code, and the answer today is a directive
saying so. If either becomes a common shape, teach `isConfirmer` about it rather than
letting directives accumulate.

## Running it

```bash
hack/check-subscribe-confirmed.sh              # every workspace module
hack/check-subscribe-confirmed.sh --self-test  # prove the check can fail

cd backend/core && go run ../tools/subconfirm/cmd/subconfirm ./...   # one module
```

The self-test plants its fixture in a package **inside `backend/core`**, so the analyzer
is exercised against the real `nats.go` and the real paho. The unit tests in
`analyzer/` use stubs — `analysistest` loads in GOPATH mode, where the real libraries are
not reachable — and those stubs are sound for what the pass reads (import path, type
name, method name), but only the self-test can catch them drifting from the libraries
they stand in for. It checks both directions: "reports a bad subscribe" is also
satisfied by a checker that reports everything.
