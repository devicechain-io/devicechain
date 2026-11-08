// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package admin

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
)

// location:read implies location:installed:read (auth.CanReadInstalledPosition), so a member who
// holds location:read covers a role or grant that names location:installed:read: the
// no-escalation check must not refuse handing out an authority the granter already holds by
// implication. The reverse is not true.
func TestLocationReadCoversLocationInstalledRead(t *testing.T) {
	read := newAuthSet([]string{string(auth.LocationRead)})
	if missing := read.missingFrom([]string{string(auth.LocationInstalledRead)}); missing != "" {
		t.Fatalf("location:read did not cover %q", missing)
	}
	installed := newAuthSet([]string{string(auth.LocationInstalledRead)})
	if missing := installed.missingFrom([]string{string(auth.LocationRead)}); missing != string(auth.LocationRead) {
		t.Fatalf("location:installed:read covered location:read (missing = %q)", missing)
	}
	// The implication covers that one authority only.
	if missing := read.missingFrom([]string{string(auth.DeviceRead)}); missing != string(auth.DeviceRead) {
		t.Fatalf("location:read covered device:read (missing = %q)", missing)
	}
}
