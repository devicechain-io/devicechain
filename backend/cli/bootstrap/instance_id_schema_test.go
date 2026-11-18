// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"helm.sh/helm/v3/pkg/action"
)

// The chart is where an underscore (or uppercase) instance id is refused: the services only
// warn on one, for compatibility, so this schema pattern is the refusal that matters for a
// plain-Helm install. Client-only rendering validates the embedded schema without an API server.
func TestSchemaInstanceIdGrammar(t *testing.T) {
	t.Parallel()
	for _, id := range []string{"foo_bar", "Foo", "foo"} {
		t.Run(id, func(t *testing.T) {
			ch, err := loadEmbeddedChart()
			if err != nil {
				t.Fatalf("loading embedded chart: %v", err)
			}
			inst := action.NewInstall(&action.Configuration{})
			inst.ReleaseName = "dc-instance-id"
			inst.Namespace = "default"
			inst.DryRun = true
			inst.ClientOnly = true
			_, err = inst.RunWithContext(t.Context(), ch, map[string]interface{}{
				"instance": map[string]interface{}{
					"id": id,
					"config": map[string]interface{}{
						"infrastructure": map[string]interface{}{
							"secrets": map[string]interface{}{
								"rootKey": base64.StdEncoding.EncodeToString(make([]byte, 32)),
							},
						},
					},
				},
			})
			if id == "foo" {
				if err != nil {
					t.Fatalf("canonical instance id refused: %v", err)
				}
			} else if msg := strings.ToLower(fmt.Sprint(err)); err == nil || !(strings.Contains(msg, "/instance/id") || strings.Contains(msg, "instance.id")) || !strings.Contains(msg, "does not match pattern") {
				t.Fatalf("instance.id=%q: got %v, want schema pattern rejection", id, err)
			}
		})
	}
}
