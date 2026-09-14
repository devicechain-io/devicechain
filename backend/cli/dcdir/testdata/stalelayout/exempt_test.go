// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package stalelayout

// A test file naming ~/.devicechain/%s. The live guard skips _test.go for the same
// reason the other one does: a test that states a layout by hand is the independent
// check on where things land, and a fixture for the layout that USED to exist has to
// be allowed to spell it. The skip protects nothing in today's tree, so without this
// file deleting it changes no result — which is the shape of an exemption everyone
// believes in and nothing has measured.
const oldLayout = "~/.devicechain/%s/infra"
