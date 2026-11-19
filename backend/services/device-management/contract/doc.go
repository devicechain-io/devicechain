// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package contract is device-management's wire contract: the shapes of the facts it publishes
// on NATS (alarm state changes, published detection rules, the device roster, device
// attributes, ownership changes, fence-set manifests), the requests other services send it
// (raise-alarm), the rows of the reconcile doors its consumers repair their projections
// against, and the small vocabularies those carry (attribute scopes, enqueue rejection codes).
//
// It exists so a consumer depends on the shapes it reads and on nothing else of this service.
// Package model re-exports every name here as an alias, so device-management's own code is
// written against model exactly as before; a service outside device-management imports this
// package and never model (backend/core/test's dmcontract guard holds that line).
//
// 🔴 IT IMPORTS NOTHING FROM device-management, AND MUST NOT. A contract that imported model
// would hand every consumer the whole of model again through the back door, which is the
// coupling this package exists to cut. It may import the shared core library.
//
// Moving a shape here changes no byte on any wire: an alias is the same type, with the same
// fields, tags, method set and encoding. A change to a shape here IS a wire change, and is
// held to the same compatibility rules as any other.
package contract
