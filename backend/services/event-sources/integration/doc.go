// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package integration is event-sources' half of an inbound integration: a tenant's authenticated
// sender of device data over HTTP, such as a LoRaWAN network server or a webhook.
//
// It holds the pieces the ingest route is built from and that carry no HTTP of their own: the
// cached pre-check that authenticates a sender before a byte of its body is read, the per-
// integration bad-credential budget, and the cached device lookup. The envelope codecs and the
// flattening of a decoded object live in core/integrationcodec, shared with device-management.
//
// Nothing here publishes. The route owns the gates and the publish, and is the only code that
// may mark an event as an integration's (see the guard in processor).
package integration
