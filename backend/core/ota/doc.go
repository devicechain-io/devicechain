// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package ota is the device update contract: the vocabulary, the wire format and the pure
// reducer that decides what a device's report does to one update attempt (ADR-012).
//
// It is a contract only. Nothing persists an Attempt, nothing carries a Report over a transport,
// and nothing here authenticates or signs an update — the digest an attempt carries is compared
// for equality and nothing more.
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
//     device volunteers matches, and when the target requires a reboot, the boot is not the one the
//     attempt started on. No command acknowledgement and no INSTALLING "success" is evidence.
//   - Deadlines are platform time, measured from the last accepted report. Silence while installing
//     or rebooting is UNKNOWN, never failure and never success.
//   - Terminal is terminal. Late progress is reported as LATE, not applied.
//
// # What VERIFIED means
//
// VERIFIED means the bytes the device holds hash to the digest the attempt names — integrity
// only. It says nothing about who produced those bytes. No trust or signature decision exists yet,
// so no state, field or verdict in this package claims an update is authentic or signed.
package ota
