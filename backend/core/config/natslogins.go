// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"
	"slices"
	"strings"

	"github.com/devicechain-io/dc-microservice/streams"
)

// Environment variables the chart projects an area's own broker login into, from a Secret that
// only that area's Deployment mounts (deploy/helm/devicechain/templates/deployment.yaml).
const (
	EnvNatsUser     = "DC_NATS_USER"
	EnvNatsPassword = "DC_NATS_PASSWORD"
)

// NatsAdminPrincipal names the broker-administration login. It belongs to no area: no Deployment
// mounts it, and it is minted so that the stream-administration step that comes later has a
// credential of its own to hold.
const NatsAdminPrincipal = "broker-admin"

// natsLoginExemptAreas are the platform areas that never open a broker connection, so they have
// no login and mount no Secret. mcp fronts GraphQL over HTTP under the caller's own token;
// ai-inference calls model providers over HTTP. Neither creates a NatsManager.
//
// 🔴 THE LIST IS OF EXEMPTIONS, NOT OF GRANTS. A new platform area (streams.PlatformAreas) gets a
// login, a Secret and a start-time requirement by default, and has to be named here to be let off.
// The failure that matters is an area that DOES use the broker and was forgotten, and this
// direction cannot produce one.
var natsLoginExemptAreas = []string{"mcp", "ai-inference"}

// NatsLoginAreas are the areas that hold a broker login of their own, sorted.
func NatsLoginAreas() []string {
	var out []string
	for _, a := range streams.PlatformAreas {
		if !slices.Contains(natsLoginExemptAreas, a) {
			out = append(out, a)
		}
	}
	slices.Sort(out)
	return out
}

// NeedsNatsLogin reports whether area is one that holds a broker login.
func NeedsNatsLogin(area string) bool {
	return streams.IsPlatformArea(area) && !slices.Contains(natsLoginExemptAreas, area)
}

// NatsLoginPrincipals are the holders of a broker login: every login area, then the administrator.
func NatsLoginPrincipals() []string {
	return append(NatsLoginAreas(), NatsAdminPrincipal)
}

// NatsLoginGeneration is the generation the logins minted today carry. A rotation adds the next
// generation beside it and retires this one, so the username names which it is.
const NatsLoginGeneration = 1

// NatsLoginUser is the broker username of a principal's login: dc-<principal>-g<generation>.
func NatsLoginUser(principal string, generation int) string {
	return fmt.Sprintf("dc-%s-g%d", principal, generation)
}

// ApplyAreaLogin makes this area connect as itself. It is called once, as the instance document is
// loaded, so every broker connection the process opens (the manager's, the affinity leader's, the
// presence canary's) presents the same login without each having to know about the change.
//
// A no-op when the document does not say the broker has per-area logins, and for an area that
// never connects (NeedsNatsLogin). Otherwise the login MUST be there, both halves: an area that
// started without one would fall back to the shared login, which is exactly what the split exists
// to end, and would do it silently.
func (a *NatsAuthConfiguration) ApplyAreaLogin(area string, getenv func(string) string) error {
	if !a.AreaLogins || !NeedsNatsLogin(area) {
		return nil
	}
	user, password := getenv(EnvNatsUser), getenv(EnvNatsPassword)
	if user == "" || password == "" {
		return fmt.Errorf("the instance configuration says the broker has a login for every area, and this "+
			"area (%s) has none: %s or %s is empty. They are projected from this area's own Secret "+
			"(dci-<instance>-nats-%s). Refusing to start rather than connect as the shared login",
			area, EnvNatsUser, EnvNatsPassword, area)
	}
	if !strings.HasPrefix(user, "dc-"+area+"-g") {
		// Any generation of THIS area's login is legitimate (a rotation adds the next beside it); a
		// different name is a Secret that belongs to another principal, and connecting with it
		// would be exactly the cross-area credential this design removes.
		return fmt.Errorf("area %s was given the broker login %q, which is not one of its own "+
			"(dc-%s-g<generation>): the Secret mounted here belongs to another principal", area, user, area)
	}
	a.User, a.Password = user, password
	return nil
}
