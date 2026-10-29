// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The served description of a command key must say what the server accepts. validateCommandKey
// admits dot-separated namespaces ("sparkplug.write"), and a description that lists only letters,
// digits, hyphens and underscores tells an integrator the key the platform itself declares is
// invalid. Every commandKey field that carries the grammar says so.
func TestTheCommandKeyDescriptionsNameTheDottedNamespace(t *testing.T) {
	re := regexp.MustCompile(`(?s)"""\s*(The key used to send[^"]*?Letters[^"]*?)\s*"""\s*commandKey: String!`)
	found := re.FindAllStringSubmatch(SchemaContent, -1)
	require.Len(t, found, 2, "the type and the create input each describe the key's grammar")
	for _, m := range found {
		assert.Contains(t, m[1], "sparkplug.write", "a description of the key grammar omits the dotted namespace")
	}
}
