// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package admission is an EXPERIMENT: admission receipts for infrastructure DeviceChain does
// not operate.
//
// A city's street lights are usually run by an incumbent central management system (a
// "head-end"). In a read-only pilot DeviceChain sits beside it, watching telemetry, and holds
// no command credentials. This package asks whether DeviceChain can still be useful on the
// write side before it is the system of record: the head-end submits what it INTENDS to do
// (a Proposal), DeviceChain checks it against a declarative Policy and answers with a signed
// Receipt that is bound to the exact proposal by digest. The head-end may act on an admitted
// receipt; DeviceChain never acts at all.
//
// Nothing here is wired into a service. It is a pure library (no IO, no clock: every time is
// an argument) so the mechanism can be judged, and shown to a head-end vendor, before anyone
// builds a served interface on it.
//
// # The risk this experiment has to answer: a decorative receipt
//
// DeviceChain does not hold the head-end's credentials, so it cannot STOP an action that was
// never admitted. A receipt the head-end can simply skip is decoration. The answer here is
// that the read-only pilot already observes what the head-end did, so Reconcile holds every
// OBSERVED execution against the receipts and classifies it:
//
//   - COVERED: a valid, admitted receipt covers the tenant, action, target, time and (when
//     observed) the parameters.
//   - REFUSED_BEFORE: a refusal, issued before the execution, for this target, time and
//     (when observed) these parameters. The head-end was told no and did it anyway.
//   - PARAMETERS_DIFFER: admitted for this target and time, but with other arguments.
//   - OUTSIDE_WINDOW: admitted for this target, but not at this time.
//   - BEFORE_RECEIPT: inside an admitted window, but observed before the receipt was issued,
//     so it cannot have acted on it.
//   - UNADMITTED: no receipt reaches it at all.
//   - MALFORMED_OBSERVATION: the observed parameters are not a JSON document this package
//     can compare, so nothing is claimed about them.
//
// A receipt whose signature does not verify, or whose proposal does not match its digest,
// never covers anything; it is reported as invalid instead. So bypass is not prevented, but
// it cannot happen quietly: it becomes a finding in the same evidence the pilot sells.
//
// # What a receipt binds
//
// The proposal digest covers the submitter, tenant, action, the SORTED target set, the
// parameters in one canonical JSON spelling, and the execution window. The receipt also
// names the policy version and the policy's own digest, the verdict, every refusal reason
// and the issue time, and all of it is signed (Ed25519) under a named key. Changing any one
// of those fields breaks the signature.
//
// # Canonical parameters, and why duplicate keys are refused
//
// Parameters are re-encoded with object keys sorted (by byte order), no insignificant
// whitespace, strings re-escaped by encoding/json, and every number kept as its literal
// text: `1` and `1.0` are different spellings and so different digests. A document with a
// duplicate object key is REFUSED rather than collapsed. Go keeps the last value of a
// duplicated key and other parsers keep the first, so `{"level":10,"level":100}` would
// digest as level 100 here while a head-end reads level 10, and the receipt would vouch
// for an action nobody admitted. Invalid UTF-8 is refused for the same reason: the decoder
// would replace it, and two different byte strings would share one digest.
//
// # Policy is declarative, and fails closed
//
// A Policy is data: per action kind, a target ceiling, a longest window, a minimum lead
// time, and numeric bounds on top-level parameters (a lighting floor is a Min on the dim
// level). An action kind with no rule is refused. There is no code hook; a predicate the
// policy cannot express is a reason to extend the data, not to accept a function.
//
// # Not in this experiment
//
//   - Binding a receipt to world state (topology, membership, capability, freshness) and
//     re-checking it at execution: that is the approval-binding work, and it belongs there.
//   - Persisting receipts, a served API, key rotation and revocation, or a transparency log.
//   - Matching observations whose identity differs from the proposal's (a telemetry field
//     that names a fixture differently from the head-end). The caller maps them first.
//   - Reconcile is O(entries x executions). Fine for a pilot's monthly evidence; an index by
//     (tenant, action, target) is the obvious next step if it is ever run continuously.
package admission
