// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package fixture is an offender inside a testdata directory. Other packages in
// this module keep testdata trees of their own, and a fixture in one is allowed to
// spell any path it likes — it is data, not code that runs. The guard must skip it,
// and this file is what makes that skip observable: without it, removing the
// testdata skip changes nothing detectable, because dcdir's own testdata happens to
// sit inside the directory the live run already exempts.
package fixture

import "path/filepath"

func path(home string) string { return filepath.Join(home, ".devicechain", "whatever") }

var _ = path
