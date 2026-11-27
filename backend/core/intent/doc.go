// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package intent is the pure compiler for a state intent: "keep one declared configuration key at
// one value over the members of one published device group" (ADR-086, AO-D1..AO-D3).
//
// It owns the v1 spec and its strict parser, the canonical form and every digest an intent's
// evidence is keyed by (SpecDigest, ValueDigest, the observation digest, InputsDigest), the
// intended-value function, the per-device diff and its four codes, chunking, the would-be status,
// the same-subject test used to find publish-time overlap candidates, the platform limits, the
// compiler build identity, and ActivationServed.
//
// It does no I/O and reads no clock: every instant is a parameter, and TestThePackageIsPure holds
// the import list to the standard library's pure packages plus core/ledger.
//
// Nothing here is served yet. The automation area and device-management's observation seam are
// built on it in later slices. The digest formats, however, become durable the moment a plan row
// stores one, which is why they are pinned by testdata/vectors.json.
//
// ActivationServed is false: no lifecycle this build knows reconciles as ACTIVE, and nothing
// exported can change that.
package intent
