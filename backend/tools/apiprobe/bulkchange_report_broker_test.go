// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/natsauth"
	"github.com/devicechain-io/dc-microservice/natsauth/permtest"
)

// The drill publishes the devices' reports as event-sources' own broker login, on a broker holding exactly
// the grants the real one is given. A JetStream publish waits for its acknowledgement on a reply inbox, and
// that login may subscribe only to event-sources' inbox: with nats.go's default inbox every publish timed out
// on a permissions violation, which is how the served drill first failed.
func TestReportsPublishAsTheForwardingAreasLogin(t *testing.T) {
	b := permtest.Start(t, "inst")
	rows := filepath.Join(t.TempDir(), "rows.tsv")
	digest := "sha256:" + strings.Repeat("ab", 32)
	if err := os.WriteFile(rows, []byte("d1\t3\t"+digest+"\nd2\t4\t"+digest+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	user := natsauth.LoginUser("event-sources")
	t.Setenv("DC_BC_NATS_PASSWORD", permtest.Password(user))
	run := func(area string) error {
		return bcReport(context.Background(), []string{"--nats", fmt.Sprintf("nats://%s:%d", b.Host, b.Port), "--user", user,
			"--instance-id", "inst", "--tenant", "t1", "--rows", rows, "--area", area, "--workers", "2"}, io.Discard)
	}
	if err := run("event-sources"); err != nil {
		t.Fatalf("publishing as event-sources' login: %v", err)
	}
	b.RequireNoViolations(t)

	// The control: the same login replying on another area's inbox is refused, so the check above can fail.
	if err := run("device-management"); err == nil {
		t.Fatal("a publish whose acknowledgement comes back on another area's inbox succeeded")
	}
	if len(b.Violations()) == 0 {
		t.Fatal("the broker recorded no permissions violation for another area's inbox")
	}
}
