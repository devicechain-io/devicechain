// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package svcclient

import (
	"fmt"
	"os"
	"strings"

	"github.com/devicechain-io/dc-microservice/auth"
)

// areaCredentialDir is where an area-bound area finds its own mint credential. A variable only so a
// test can point it at a temporary directory; nothing else assigns it.
var areaCredentialDir = auth.AreaMintCredentialDir

// readAreaCredential reads an area-bound credential, on every mint so a rotated one is used as soon
// as the kubelet updates the mounted file. An error names the PATH and never the content.
func readAreaCredential(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("svcclient: the area-bound mint credential is not readable at %s: %w", path, err)
	}
	c := strings.TrimSpace(string(b))
	if len(c) < auth.MinAreaCredentialLen {
		return "", fmt.Errorf("svcclient: the area-bound mint credential at %s is shorter than %d characters", path, auth.MinAreaCredentialLen)
	}
	return c, nil
}
