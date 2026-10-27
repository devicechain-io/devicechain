// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package ota is the device update contract: the vocabulary, the wire format and the pure
// reducer that decides what a device's report does to one update attempt (ADR-012).
//
// It is a contract only. This package persists nothing and carries nothing over a transport:
// update-management persists the Attempt and consumes the Report, and nothing here authenticates
// or signs an update — the digest an attempt carries is compared for equality and nothing more.
//
// # State mapping
//
// The device reports stages in its own words; the platform maps them to states. Names marked
// "proposed" are an amendment to the accepted state machine, pending maintainer confirmation.
//
//	device stage   platform state  who writes it                              status
//	-------------  --------------  ------------------------------------------  ---------------------
//	(none)         QUEUED          platform, when the attempt is created       ADR-012
//	RECEIVED       INITIATED       device                                      ADR-012
//	DOWNLOADING    DOWNLOADING     device (+bytes/total)                       ADR-012
//	DOWNLOADED     DOWNLOADED      device                                      ADR-012
//	VERIFIED       VERIFIED        device; the digest it verified must equal   ADR-012, constrained
//	                               the attempt's digest
//	INSTALLING     UPDATING        device                                      ADR-012
//	REBOOTING      REBOOTING       device                                      proposed
//	RUNNING        UPDATED         platform, ONLY on valid confirmation        ADR-012, constrained
//	                               evidence
//	FAILED         FAILED          device (+code, detail)                      ADR-012
//	(none)         TIMED_OUT       platform: a deadline elapsed at or before   proposed (terminal)
//	                               VERIFIED
//	(none)         UNKNOWN         platform: a deadline elapsed at UPDATING    proposed
//	                               or REBOOTING; leaves only on evidence
//	ABANDONED      CANCELLED       platform from QUEUED; otherwise on the      proposed (terminal)
//	                               device's ABANDONED after a cancel request,
//	                               at or before VERIFIED
//
// # Rules the reducer enforces
//
//   - Identity comes from the subject, binding from the payload. A report carries no device
//     identity: the device is whoever the broker-confined subject says. Every progress report names
//     the attempt, assignment and artifact digest it belongs to, and a mismatch is rejected.
//   - Forward only. A later stage may skip lost reports; an earlier one is a REGRESSION. The same
//     stage with a higher seq is progress; the same seq again is a DUPLICATE.
//   - Only confirmation evidence reaches UPDATED: the target version is running, any digest the
//     device volunteers matches, and when the target requires a reboot, the boot differs from the
//     install boot (the boot of the first report at UPDATING or later; the last reported boot when no
//     install report arrived). An unrelated restart earlier in the attempt does not count. No command acknowledgement and no INSTALLING "success" is evidence.
//   - UNKNOWN leaves on evidence, or when an operator explicitly abandons it (Abandon): that ends it
//     CANCELLED with a stated reason and says nothing about what the device actually runs.
//   - Deadlines are platform time, measured from the last accepted report. Silence while installing
//     or rebooting is UNKNOWN, never failure and never success.
//   - A repeated seq with the same content is a DUPLICATE; with different content it is a CONFLICT.
//     Report keys must match exactly (no case folding, no repeats).
//   - Terminal is terminal. Late progress is reported as LATE, not applied.
//
// # What VERIFIED means
//
// VERIFIED means the bytes the device holds hash to the digest the attempt names — integrity
// only. It says nothing about who produced those bytes. Authenticity is the device's own check of
// the detached signature a SIGNED assignment carries; a device that refuses one reports a FAILED
// stage with result code SIGNATURE_INVALID or UNKNOWN_SIGNER. A bad or unknown signature can only be
// found once the device holds the bytes, so it is reported after DOWNLOADED, like VERIFICATION_FAILED;
// a device that requires signatures may refuse an UNVERIFIED assignment sooner, right after RECEIVED. A
// device may report FAILED at any stage. No state in this package claims an update is authentic: the platform
// relays the signature, the device decides.
package ota
