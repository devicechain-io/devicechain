// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package partialupdatetest is the platform-wide harness for the three-state update
// semantic: an omitted field leaves the stored value alone, an explicit null clears it,
// and a value sets it.
//
// # Why it is an importable package and not a test file
//
// It is a non-test package that imports "testing", the way net/http/httptest does, for
// one reason: a `_test.go` file cannot be used by another MODULE. The semantic it
// certifies is one platform rule implemented in eight services, and the alternative to
// sharing the harness is copying it — which is exactly the drift the harness was built
// to end. The header on Run says what that drift looked like the first time.
//
// # What a service supplies
//
//   - a fixture that builds its own *Api over a throwaway database (NewSQLiteDB does the
//     database half, which is where the traps are);
//   - a Family per converted entity, declaring its fields ONCE, as data;
//   - the tenant context its models expect.
//
// Everything else — the properties, the anti-vacuity controls, the exhaustiveness check
// against the request TYPE — is here and is identical everywhere.
//
// # The two entry points
//
//	Run                                  drives every property over every family
//	AssertEveryUpdateTakesADedicatedRequest  derives the surface being certified from
//	                                     the service's own *Api, so a family added on
//	                                     the old full-replace shape fails on the day it
//	                                     is added rather than the day it is noticed
//
// They are separate because they answer different questions. Run asks whether the
// converted families BEHAVE; the guard asks whether the set of converted families is
// still the whole set.
package partialupdatetest
