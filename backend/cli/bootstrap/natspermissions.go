// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/natsauth"
)

// BrokerPermissionsJSON is the generated permissions of every static broker login of an instance,
// as the one JSON document the broker module renders into its users verbatim.
//
// It is GENERATED, from the same stream and bucket declarations the services are held to
// (natsauth.ServiceUsers), at the moment the broker is rendered, so no second description of what
// a login may do exists to drift. `dcctl broker permissions` prints this same value for an
// installation that brings its own broker.
//
// Compact and unescaped: json.Marshal would write ">" as >, which is the same JSON but not what
// anyone reading the printed document, or nats-server's own configuration parser, expects.
//
// withSharedLogin adds the transitional `dc_service` block, for the one broker configuration that
// still has that login (an upgraded instance before its retirement). Every other broker, a fresh
// install and a retired instance included, is rendered without it.
func BrokerPermissionsJSON(instance string, withSharedLogin bool) (string, error) {
	var (
		users map[string]map[string]natsauth.Permissions
		err   error
	)
	if withSharedLogin {
		users, err = natsauth.ServiceUsersWithSharedLogin(instance, config.NatsLoginAreas())
	} else {
		users, err = natsauth.ServiceUsers(instance)
	}
	if err != nil {
		return "", fmt.Errorf("generating the broker permissions for instance %q: %w", instance, err)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(users); err != nil {
		return "", err
	}
	return string(bytes.TrimSpace(buf.Bytes())), nil
}

// brokerPermissionsVarFile is the variable file the permissions document travels in. It is a FILE
// and not a -var because the document is large (tens of kilobytes: every area's stream, durable and
// bucket grants) and a command line is not: Windows refuses a process whose command line passes
// 32 KiB, and the document grows with every stream and bucket added to the catalogs. OpenTofu loads
// a *.auto.tfvars.json in the working directory on every plan and apply, so the one file reaches
// both a bootstrap and an upgrade.
const brokerPermissionsVarFile = "dcctl-broker-permissions.auto.tfvars.json"

// writeBrokerPermissionsVarFile writes (or, for a broker with no authentication, removes) the file
// that hands the instance root its nats_service_users variable. It is written on EVERY run, from
// the catalogs of the dcctl doing the run, so an upgrade renders the grants of the release it is
// installing and a stale file from an earlier run cannot outlive it.
//
// The broker module refuses to render a login it has no block for, so a missing or unreadable file
// stops the apply rather than weakening it.
func writeBrokerPermissionsVarFile(rootdir string, st *State) error {
	path := filepath.Join(rootdir, brokerPermissionsVarFile)
	if st.Values["natsCalloutIssuerPublic"] == "" {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	doc, err := BrokerPermissionsJSON(st.Instance, sharedLoginRetainedFor(st))
	if err != nil {
		return err
	}
	raw, err := json.Marshal(map[string]string{"nats_service_users": doc})
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return fmt.Errorf("writing the broker permissions for instance %q: %w", st.Instance, err)
	}
	return nil
}
