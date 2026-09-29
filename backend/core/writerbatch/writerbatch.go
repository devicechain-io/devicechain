// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package writerbatch holds the bounds on a batching writer's batch: how many messages one
// transaction may commit and how long a writer may wait for its batch to fill.
//
// It is its own package, importing nothing, so that code which only needs the numbers —
// core/messaging's CollectBatch tests among it — does not import core/rdb for them. A module
// that depends on core resolves the dependencies of the tests of every core package it
// builds, so one such import put the database stack's checksums into modules that do not
// use core/rdb. core/rdb enforces these bounds on configuration (rdb.CheckWriterBatch).
package writerbatch

// MaxSize caps the messages one batching writer commits in one transaction.
//
// It is NOT a statement-size bound, and PostgreSQL's 65535-parameter limit is not what caps
// it. event-management runs each message's own statements one after another inside the
// batch's transaction, so a statement's parameters are bounded by one message; device-state
// merges a batch into multi-row upserts chunked at a fixed row count, so its statements are
// bounded by that chunk (and its lock by the batch's distinct devices, at most this cap).
// Neither comes near the limit at any batch size this cap allows.
//
// What it bounds is how long a writing transaction can be, and that has two costs:
//   - the erasure fence's first answer is remembered for the whole transaction
//     (core/rdb's tenant_fence.go), so a longer one widens the window its argument accounts for;
//   - a batch that does not commit is written again: the rest of the batch re-runs once
//     for each message refused inside it, and a failure no message caused writes every one
//     of them again on its own.
//
// And nothing measured has shown a larger cap would help. On a three-node cloud cluster,
// event-management at 10 writers stopped rising near 6,000 events a second with batches
// averaging about 21 at 6,000 and 28 to 30 above it, under this cap. device-state's batches did reach it (60 to 63, with a
// 25 ms linger) in the same run; a larger cap is a change to make from a measurement of
// that, not ahead of one.
const MaxSize = 64

// MaxLingerMillis caps how long a batching writer may wait for its batch to fill.
const MaxLingerMillis = 1000
