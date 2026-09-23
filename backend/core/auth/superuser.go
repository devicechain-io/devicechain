// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package auth

// DefaultSuperuserEmail is the email of the global superuser user-management seeds on
// first start, when its configuration names no other.
//
// It lives in core because it is a contract with more than one module: user-management
// defaults to it, and dcctl's bootstrap report, `dcctl sim` and the maintainer drills
// all sign in as it. Each of those used to carry its own copy of the literal, and a
// copy is what drifts.
//
// 🔴 THERE IS DELIBERATELY NO DEFAULT PASSWORD BESIDE IT. The email is not a secret;
// the password is, and a default password is the same credential on every instance
// ever built. user-management reads the seed password from its environment, where
// dcctl projects a value it generated per instance, and refuses to seed without one.
const DefaultSuperuserEmail = "superuser@devicechain.local"
