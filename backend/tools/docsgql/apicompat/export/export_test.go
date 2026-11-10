// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package export

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/devicechain-io/dc-docsgql/apicompat/decl"
	"github.com/devicechain-io/dc-docsgql/apicompat/topics"
)

const uplinkTopic = "{instance}/{tenant}/devices/{device}/uplink"

func TestDeviceTopicsIncludesRealOptInUplink(t *testing.T) {
	root, err := filepath.Abs("../../../../..")
	if err != nil {
		t.Fatal(err)
	}
	l := exportLayout(t, root)
	if !slices.Contains(l.Publish, uplinkTopic) {
		t.Fatalf("opt-in uplink missing from real grant export: %+v", l)
	}
	if slices.Contains(l.Subscribe, uplinkTopic) {
		t.Fatal("uplink must be publish-only")
	}
}

func TestDeviceTopicsCompilesBeforeAndAfterUplinkOption(t *testing.T) {
	// Compile the actual injected exporter against both API generations. The old
	// API is deliberately not variadic and has no WithDeviceUplink symbol.
	base := exportLayout(t, topicTree(t, false))
	head := exportLayout(t, topicTree(t, true))
	if slices.Contains(base.Publish, uplinkTopic) || !slices.Contains(head.Publish, uplinkTopic) {
		t.Fatalf("base=%+v head=%+v", base, head)
	}
	changes := topics.Compare(base, head)
	if len(changes) != 1 || changes[0].Kind != "topic-added" || changes[0].Severity != decl.Safe || changes[0].Coord != "publish:"+uplinkTopic {
		t.Fatalf("opt-in addition must be Safe: %+v", changes)
	}
	changes = topics.Compare(head, base)
	if len(changes) != 1 || changes[0].Kind != "topic-removed" || changes[0].Severity != decl.Breaking {
		t.Fatalf("later uplink removal must break compatibility: %+v", changes)
	}
}

func exportLayout(t *testing.T, root string) topics.Layout {
	t.Helper()
	out := filepath.Join(t.TempDir(), "topics.json")
	if err := DeviceTopics(root, out); err != nil {
		t.Fatal(err)
	}
	l, err := topics.Read(out)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func topicTree(t *testing.T, uplink bool) string {
	t.Helper()
	root := t.TempDir()
	write := func(name, src string) {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.work", "go 1.26.5\n\nuse ./backend/core\n")
	write("backend/core/go.mod", "module github.com/devicechain-io/dc-microservice\n\ngo 1.26.5\n")
	write("backend/core/messaging/topics.go", `// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import "strings"

func SubjectToMqttTopic(s string) string { return strings.ReplaceAll(s, ".", "/") }
`)
	args, apply, option := "", "", ""
	if uplink {
		args = ", opts ...func(*Permissions)"
		apply = "for _, o := range opts { o(&p) }"
		option = `func WithDeviceUplink(on bool) func(*Permissions) {
			return func(p *Permissions) {
				if on { p.Pub.Allow = append(p.Pub.Allow, "INSTANCE.TENANT.devices.DEVICE.uplink") }
			}
		}`
	}
	write("backend/core/natsauth/grant.go", fmt.Sprintf(`// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package natsauth

type Rules struct { Allow []string }
type Permissions struct { Pub, Sub Rules }

func DevicePermissions(instance, tenant, device string%s) (Permissions, error) {
	p := Permissions{
		Pub: Rules{Allow: []string{"INSTANCE.TENANT.devices.DEVICE.events"}},
		Sub: Rules{Allow: []string{"INSTANCE.TENANT.device-commands.DEVICE", "$MQTT.sub.>"}},
	}
	%s
	return p, nil
}
%s
`, args, apply, option))
	return root
}
