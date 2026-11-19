// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package identity is device identity and credentials, being carved out of package model:
// trust anchors, the x509 revocation store, the device credential row and its vocabulary, the
// pure x509 checks and rules, and (in later steps) provisioning, claims and session kicks.
//
// The carve goes leaf first. The entities moved so far depend on nothing else in model. The
// code that works on them through model's *Api stays in model for now. model re-exports every
// name here as an alias, so the rest of the service is unchanged, and the tables, encodings and
// served shapes are the same types as before. The one exception is the credential: its row is
// here, and model.DeviceCredential is a struct that embeds it and adds the Device association,
// which belongs to the registry. GORM flattens the embedding, so the table and its columns are
// unchanged.
//
// 🔴 IT IMPORTS NOTHING OF device-management. model imports it, so an import back would be a
// cycle, and any other edge would make this a second way into the domain.
// device-management's package_dag_test.go refuses every such edge. When a later step needs
// something from model, that thing moves here, or it is passed in, as model passes the
// transaction (*gorm.DB) to keep a device, credential and session-kick write in one
// transaction.
package identity
