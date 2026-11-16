// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package product

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/userclient"
)

func TestConnectorDestinationEditResuppliesCredential(t *testing.T) {
	for _, destination := range []bool{true, false} {
		t.Run(map[bool]string{true: "destination", false: "topic"}[destination], func(t *testing.T) {
			f, dir := seeded(t)
			editBundle(t, dir, func(b *Bundle) {
				raw := string(b.Connectors[0].Config)
				if destination {
					raw = strings.ReplaceAll(raw, "broker", "other-broker")
				} else {
					raw = strings.ReplaceAll(raw, "alerts", "other-topic")
				}
				b.Connectors[0].Config = Doc(raw)
			})
			other := map[string]string{"DC_CONNECTOR_AUDIT_SINK": "replacement"}
			p := apply(t, f, dir, env(other))
			want := secretValue
			if destination {
				want = "replacement"
				if len(p.Notes) == 0 {
					t.Fatal("no re-supply plan note")
				}
			}
			if f.secrets["audit-sink"] != want {
				t.Fatalf("credential=%q want %q", f.secrets["audit-sink"], want)
			}
		})
	}
}

type credentialRefusingClient struct{ Client }

func (c credentialRefusingClient) Query(ctx context.Context, base, query string, vars map[string]any, out any) error {
	if strings.Contains(query, "updateConnector") {
		return &userclient.GraphQLError{Messages: []string{"re-supply credential"}, Codes: []string{"SECRET_RESUPPLY_REQUIRED"}}
	}
	return c.Client.Query(ctx, base, query, vars, out)
}

func TestConnectorMissingCredentialGivesHandleAtApply(t *testing.T) {
	f, dir := seeded(t)
	editBundle(t, dir, func(b *Bundle) {
		b.Connectors[0].Config = Doc(strings.ReplaceAll(string(b.Connectors[0].Config), "broker", "other-broker"))
	})
	bundle, err := Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	p, err := PlanBundle(context.Background(), credentialRefusingClient{Client: f}, ep(), bundle, env(map[string]string{}))
	if err != nil {
		t.Fatal(err)
	}
	err = p.Apply(context.Background(), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "set env:DC_CONNECTOR_AUDIT_SINK and re-run") {
		t.Fatalf("error=%v", err)
	}
	if f.secrets["audit-sink"] != secretValue {
		t.Fatal("refusal altered credential")
	}
}
