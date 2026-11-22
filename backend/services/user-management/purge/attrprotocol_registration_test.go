// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package purge

import (
	"os"
	"strings"
	"testing"
)

// The purge executor must mark its sweep transaction for the device attribute writer protocol
// (LiftFence deletes no attribute row and needs no mark). A later release refuses an unmarked
// write of a device's attribute rows, so a purge that forgot would fail closed there; this pins
// the call here, where no test runs that Postgres guard. It must register before the sweep.
func TestRelationalRegistersTheAttributeProtocolOnEverySweep(t *testing.T) {
	raw, err := os.ReadFile("relational.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	if n := strings.Count(src, "attrprotocol.RegisterPurge(plan)"); n != 1 {
		t.Fatalf("relational.go registers the attribute protocol %d times; Erase must register exactly once", n)
	}
	start := strings.Index(src, "func (r *Relational) Erase(")
	if start < 0 {
		t.Fatal("Erase not found")
	}
	body := src[start:]
	if next := strings.Index(body[1:], "\nfunc "); next >= 0 {
		body = body[:next+1]
	}
	reg := strings.Index(body, "attrprotocol.RegisterPurge(plan)")
	sweep := strings.Index(body, "tenantpurge.Sweep(")
	if reg < 0 || sweep < 0 || reg > sweep {
		t.Fatal("Erase must register the attribute protocol on its plan before sweeping it")
	}
}
