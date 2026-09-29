// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"reflect"
	"strings"
	"testing"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/releaseutil"
	"sigs.k8s.io/yaml"
)

// The opt-in profiling listener, as the chart renders it: one environment variable on
// ONE area's Deployment, never a port. The service side — that the variable starts a
// listener, and that the traffic port never serves one — is in core's profiler tests.

const profilerEnvName = "DC_PROFILER_ADDRESS"

// renderedPod is what these tests read out of one Deployment and its Service.
type renderedPod struct {
	env            map[string]string
	containerPorts []int
	servicePorts   []int
}

// renderAny renders the embedded chart over exactly the values given (no instance
// block is added, unlike renderChart), returning the manifest or the render error.
func renderAny(t *testing.T, vals map[string]interface{}) (string, error) {
	t.Helper()
	inst := action.NewInstall(&action.Configuration{})
	inst.ReleaseName = "dc-dctest"
	inst.Namespace = "default"
	inst.DryRun = true
	inst.ClientOnly = true
	inst.APIVersions = []string{"monitoring.coreos.com/v1"}
	rel, err := inst.RunWithContext(t.Context(), chartForTest(t), vals)
	if err != nil {
		return "", err
	}
	return rel.Manifest, nil
}

// podsOf decodes every area's Deployment env and ports, and its Service ports.
func podsOf(t *testing.T, manifest string) map[string]*renderedPod {
	t.Helper()
	pods := map[string]*renderedPod{}
	pod := func(name string) *renderedPod {
		if pods[name] == nil {
			pods[name] = &renderedPod{env: map[string]string{}}
		}
		return pods[name]
	}
	for _, doc := range releaseutil.SplitManifests(manifest) {
		var obj struct {
			Kind     string `json:"kind"`
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Spec struct {
				Ports []struct {
					Port int `json:"port"`
				} `json:"ports"`
				Template struct {
					Spec struct {
						Containers []struct {
							Env []struct {
								Name  string `json:"name"`
								Value string `json:"value"`
							} `json:"env"`
							Ports []struct {
								ContainerPort int `json:"containerPort"`
							} `json:"ports"`
						} `json:"containers"`
					} `json:"spec"`
				} `json:"template"`
			} `json:"spec"`
		}
		if err := yaml.Unmarshal([]byte(doc), &obj); err != nil {
			t.Fatalf("decoding a rendered manifest: %v\n%s", err, doc)
		}
		switch obj.Kind {
		case "Deployment":
			p := pod(obj.Metadata.Name)
			for _, c := range obj.Spec.Template.Spec.Containers {
				for _, e := range c.Env {
					p.env[e.Name] = e.Value
				}
				for _, cp := range c.Ports {
					p.containerPorts = append(p.containerPorts, cp.ContainerPort)
				}
			}
		case "Service":
			p := pod(obj.Metadata.Name)
			for _, sp := range obj.Spec.Ports {
				p.servicePorts = append(p.servicePorts, sp.Port)
			}
		}
	}
	if pods["device-management"] == nil || pods["event-sources"] == nil {
		t.Fatalf("the render has no device-management or event-sources Deployment; every assertion below would be vacuous")
	}
	return pods
}

func profilerOn(area string, profiler map[string]interface{}) map[string]interface{} {
	return map[string]interface{}{
		"functionalAreas": map[string]interface{}{
			area: map[string]interface{}{"profiler": profiler},
		},
	}
}

// OFF BY DEFAULT: no area's pod carries the variable, so no service starts a listener.
func TestNoAreaIsProfiledByDefault(t *testing.T) {
	manifest, err := renderChart(t, nil)
	if err != nil {
		t.Fatalf("rendering the default chart: %v", err)
	}
	for area, p := range podsOf(t, manifest) {
		if v, ok := p.env[profilerEnvName]; ok {
			t.Errorf("%s carries %s=%q in a default install", area, profilerEnvName, v)
		}
	}

	// Negative control: the same reader does see the variable when it is there.
	manifest, err = renderChart(t, profilerOn("device-management", map[string]interface{}{"enabled": true}))
	if err != nil {
		t.Fatalf("rendering with the profiler on: %v", err)
	}
	if _, ok := podsOf(t, manifest)["device-management"].env[profilerEnvName]; !ok {
		t.Fatal("the reader cannot see the variable even when it is rendered, so the check above proves nothing")
	}
}

// ON FOR ONE AREA: that area's pod gets the default loopback address, and no other pod
// gets anything, so turning it on restarts that service alone.
func TestEnablingOneAreaReachesThatPodAlone(t *testing.T) {
	manifest, err := renderChart(t, profilerOn("device-management", map[string]interface{}{"enabled": true}))
	if err != nil {
		t.Fatalf("rendering with the profiler on: %v", err)
	}
	pods := podsOf(t, manifest)
	if got := pods["device-management"].env[profilerEnvName]; got != "127.0.0.1:6060" {
		t.Errorf("device-management %s = %q, want the loopback default 127.0.0.1:6060", profilerEnvName, got)
	}
	for area, p := range pods {
		if area == "device-management" {
			continue
		}
		if v, ok := p.env[profilerEnvName]; ok {
			t.Errorf("%s carries %s=%q although only device-management was enabled", area, profilerEnvName, v)
		}
	}

	manifest, err = renderChart(t, profilerOn("device-management",
		map[string]interface{}{"enabled": true, "address": "0.0.0.0:7070"}))
	if err != nil {
		t.Fatalf("rendering with an explicit address: %v", err)
	}
	if got := podsOf(t, manifest)["device-management"].env[profilerEnvName]; got != "0.0.0.0:7070" {
		t.Errorf("the configured address did not reach the pod: got %q", got)
	}

	// enabled: false is off, address or not.
	manifest, err = renderChart(t, profilerOn("device-management",
		map[string]interface{}{"enabled": false, "address": "0.0.0.0:7070"}))
	if err != nil {
		t.Fatalf("rendering with the profiler explicitly off: %v", err)
	}
	if v, ok := podsOf(t, manifest)["device-management"].env[profilerEnvName]; ok {
		t.Errorf("enabled: false still rendered %s=%q", profilerEnvName, v)
	}
}

// NEVER A PORT: the listener is not a container port and not a Service port, so the
// Service and the ingress cannot route to it.
func TestTheProfilerIsNeverAPortOfThePodOrItsService(t *testing.T) {
	manifest, err := renderChart(t, profilerOn("device-management", map[string]interface{}{"enabled": true}))
	if err != nil {
		t.Fatalf("rendering with the profiler on: %v", err)
	}
	dm := podsOf(t, manifest)["device-management"]
	if !reflect.DeepEqual(dm.servicePorts, []int{8080}) {
		t.Errorf("device-management's Service ports are %v, want exactly [8080]", dm.servicePorts)
	}
	if !reflect.DeepEqual(dm.containerPorts, []int{8080}) {
		t.Errorf("device-management's container ports are %v, want exactly [8080]", dm.containerPorts)
	}
	if n := strings.Count(manifest, "6060"); n != 1 {
		t.Errorf("6060 appears %d times in the manifest; it should appear once, in the one env value", n)
	}
}

// Every dcctl install runs with instance.existingSecret, where the chart does not
// write the instance document. The switch is a pod setting, so it must work there
// too: this is the path a benchmark on a dcctl-installed instance takes.
func TestTheProfilerWorksOnAnInstanceWhoseConfigIsInASecret(t *testing.T) {
	_, install := composedForTest(t, composeStateForTest())
	mergeFunctionalArea(install, "device-management",
		map[string]interface{}{"profiler": map[string]interface{}{"enabled": true}})

	manifest, err := renderAny(t, install)
	if err != nil {
		t.Fatalf("rendering dcctl's install values with the profiler on: %v", err)
	}
	if got := podsOf(t, manifest)["device-management"].env[profilerEnvName]; got != "127.0.0.1:6060" {
		t.Errorf("under an external config Secret, device-management %s = %q, want 127.0.0.1:6060", profilerEnvName, got)
	}
}

// Refusals at render: an area that is not deployed (the setting would do nothing), a
// port the pod already serves (the service's own listener would then fail to bind, and
// its error would name the wrong one), and a misspelled key.
func TestProfilerSettingsThatCannotWorkAreRefused(t *testing.T) {
	for _, tc := range []struct {
		name string
		vals map[string]interface{}
		want string
	}{
		{"undeployed area", profilerOn("mcp", map[string]interface{}{"enabled": true}),
			"functionalAreas.mcp.profiler.enabled is true, but mcp is not deployed"},
		{"the service port", profilerOn("device-management",
			map[string]interface{}{"enabled": true, "address": "127.0.0.1:8080"}),
			"uses port 8080, which the device-management pod already serves"},
		{"an extra port", profilerOn("event-sources",
			map[string]interface{}{"enabled": true, "address": "0.0.0.0:8081"}),
			"uses port 8081, which the event-sources pod already serves over TCP"},
		{"a misspelled key", profilerOn("device-management",
			map[string]interface{}{"enabeld": true}),
			"enabeld"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := renderChart(t, tc.vals)
			if err == nil {
				t.Fatal("rendered without complaint")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("refused, but not for this reason:\n  got:  %v\n  want: %s", err, tc.want)
			}
		})
	}
}

// A UDP port is not the profiler's: the listener is TCP, so a profiler on the number of
// a UDP extraPort (lwm2m-ingest's CoAPS is one) binds without conflict and is rendered.
// A collision check that compared numbers alone refused it, saying the pod already
// served that port.
func TestAProfilerMayShareANumberWithAUDPPort(t *testing.T) {
	vals := map[string]interface{}{
		"functionalAreas": map[string]interface{}{
			"event-sources": map[string]interface{}{
				"extraPorts": []interface{}{
					map[string]interface{}{"name": "udp-in", "port": 9999, "protocol": "UDP"},
				},
				"profiler": map[string]interface{}{"enabled": true, "address": "127.0.0.1:9999"},
			},
		},
	}
	manifest, err := renderChart(t, vals)
	if err != nil {
		t.Fatalf("a profiler on a UDP port's number was refused: %v", err)
	}
	if got := podsOf(t, manifest)["event-sources"].env[profilerEnvName]; got != "127.0.0.1:9999" {
		t.Errorf("event-sources %s = %q, want 127.0.0.1:9999", profilerEnvName, got)
	}
}

// dcctl upgrade carries lwm2m-ingest's block from the previous release whole. The
// profiler must not ride along with it, or that would be the one area an upgrade left
// profiling.
func TestAnUpgradeDoesNotCarryAProfilerForward(t *testing.T) {
	previous := aPreviousRelease()
	lwm2m := previous["functionalAreas"].(map[string]interface{})["lwm2m-ingest"].(map[string]interface{})
	lwm2m["profiler"] = map[string]interface{}{"enabled": true}

	vals := map[string]interface{}{}
	carryReleaseValues(vals, previous)

	carried, _ := vals["functionalAreas"].(map[string]interface{})["lwm2m-ingest"].(map[string]interface{})
	if carried == nil || carried["config"] == nil {
		t.Fatalf("the LwM2M block was not carried at all: %v", vals)
	}
	if p, ok := carried["profiler"]; ok {
		t.Errorf("the profiler was carried into the upgrade: %v", p)
	}
	if _, ok := lwm2m["profiler"]; !ok {
		t.Error("dropping it edited the previous release's own record")
	}
}
