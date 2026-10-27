// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package buildinfo holds the two facts a firmware image knows about itself. They are set by the
// linker (-ldflags -X), never read from configuration and never copied from an assignment: the
// version a device reports as running is the one compiled into the binary that is running.
package buildinfo

// Version is the semantic version compiled into this image.
var Version = "0.0.0"

// Product names the kind of device this image is for. An image built for another product must
// never be installed, whatever version it claims.
var Product = "ota-device-example"
