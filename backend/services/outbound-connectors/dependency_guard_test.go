// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// The connectors service must not LINK machinery that reaches for credentials or
// destinations a tenant did not author: an embedded stream engine with dialers of its own,
// the AWS configuration loader (environment variables, shared files), the instance-metadata
// client, or the STS/SSO token exchanges. A client built from a literal configuration
// cannot consult what is not in the binary.
//
// 🔴 It lists THIS package — package main, the service binary — and not a test binary of
// the publish package. A guard over the publish test binary would miss a link added
// anywhere else in the service and go green over a binary that ships the thing.
func TestTheBinaryLinksNoAmbientCloudMachinery(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps: %v\n%s", err, out)
	}
	deps := strings.Fields(string(out))

	// Anchors: packages the binary certainly links. If they are missing, the listing is
	// not this binary's, and every absence below would mean nothing.
	for _, anchor := range []string{
		"github.com/devicechain-io/dc-outbound-connectors",
		"github.com/devicechain-io/dc-outbound-connectors/processor",
	} {
		if !slices.Contains(deps, anchor) {
			t.Fatalf("the dependency listing does not contain %s; it is not this binary's", anchor)
		}
	}

	forbidden := []string{
		"github.com/warpstreamlabs/bento",
		"github.com/IBM/sarama",
		"github.com/aws/aws-sdk-go-v2/config",
		"github.com/aws/aws-sdk-go-v2/feature/ec2/imds",
		"github.com/aws/aws-sdk-go-v2/service/sts",
		"github.com/aws/aws-sdk-go-v2/service/sso",
		"github.com/aws/aws-sdk-go-v2/service/ssooidc",
	}
	var linked []string
	for _, d := range deps {
		for _, f := range forbidden {
			if d == f || strings.HasPrefix(d, f+"/") {
				linked = append(linked, d)
				break
			}
		}
	}
	if len(linked) > 0 {
		t.Fatalf("the service binary links %d forbidden package(s), e.g. %v",
			len(linked), linked[:min(5, len(linked))])
	}
}
