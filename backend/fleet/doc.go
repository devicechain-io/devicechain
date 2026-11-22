// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package fleet is the home of the fleet manager's data model and the exclusion protocol that
// guards each fleet cluster's single writer. It is a different instance type from the tenant
// areas under backend/services: its tables are not tenant-scoped, and nothing here is served.
//
// This module is Preview. Nothing in it is part of a covered interface (GraphQL, the device
// wire, dcctl) until a fleet API ships.
package fleet
