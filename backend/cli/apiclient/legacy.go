// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package apiclient supplies dcctl's conservative, same-major legacy API contract.
package apiclient

import (
	"embed"
	"net/url"
	"strings"
)

// These are the unmodified served schemas from the v1.7.0 release tag. The
// published policy makes that contract safe on v1.7 and v1.8 instances, even
// though those releases cannot report schema capabilities. Unknown functionality
// is refused rather than inferred from the version of dcctl running locally.
//
//go:embed schemas/*.graphql
var legacySchemas embed.FS

// LegacySchema resolves an ingress GraphQL URL to the correct area AND plane.
// It returns no contract for an unrecognized route. An absent legacy capability
// means "cannot confirm", not proof that the instance lacks it.
func LegacySchema(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil {
		return ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 3 || len(parts) > 4 || parts[0] != "api" || parts[len(parts)-1] != "graphql" {
		return ""
	}
	name := parts[1]
	if len(parts) == 4 {
		if parts[2] != "admin" && parts[2] != "settings" {
			return ""
		}
		name += "-" + parts[2]
	}
	data, err := legacySchemas.ReadFile("schemas/" + name + ".graphql")
	if err != nil {
		return ""
	}
	return string(data)
}
