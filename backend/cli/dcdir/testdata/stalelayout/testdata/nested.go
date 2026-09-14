// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package nested sits in a testdata directory INSIDE the fixture tree, so the walk's
// testdata skip has something to skip that the live run's dcdir exemption does not
// already cover. Without it, deleting the skip changes no result anywhere: dcdir's own
// testdata is inside dcdir, which the live run exempts wholesale.
package nested

// stale names ~/.devicechain/<instance>, which the guard would report if it read here.
const stale = "~/.devicechain/%s/infra"
