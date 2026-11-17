// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"fmt"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

// Resolve numeric Gateway backends through their Services, then allow only the
// public port names. Ingress backends already use names.
func publicBackendPort(name string, port interface{}, services map[string]map[string]string) bool {
	p := fmt.Sprint(port)
	if p == fmt.Sprint(core.OpsPort) {
		return false
	}
	if resolved, ok := services[name][p]; ok {
		p = resolved
	}
	return p == "graphql" || p == "http" || p == "http-ingest"
}

func TestChartReservesOperationsPort(t *testing.T) {
	for _, vals := range []map[string]interface{}{
		{"service": map[string]interface{}{"port": 8083}},
		{"functionalAreas": map[string]interface{}{"event-sources": map[string]interface{}{"extraPorts": []interface{}{map[string]interface{}{"name": "http-ingest", "port": 8083, "protocol": "UDP"}}}}},
		{"functionalAreas": map[string]interface{}{"event-sources": map[string]interface{}{"extraPorts": []interface{}{map[string]interface{}{"name": "http-ingest", "port": 8083}}}}},
		{"functionalAreas": map[string]interface{}{"event-sources": map[string]interface{}{"extraPorts": []interface{}{map[string]interface{}{"name": "ops", "port": 8084}}}}},
	} {
		_, err := renderChart(t, vals)
		require.ErrorContains(t, err, "reserved for operations")
	}
	_, err := renderChart(t, map[string]interface{}{"functionalAreas": map[string]interface{}{"event-sources": map[string]interface{}{"extraPorts": []interface{}{map[string]interface{}{"name": "http-ingest", "port": 8081}}}}})
	require.NoError(t, err)
}

func TestPublicBackendPortRejectsOps(t *testing.T) {
	services := map[string]map[string]string{"area": {"8080": "graphql", "8083": "ops"}}
	require.False(t, publicBackendPort("area", "ops", services))
	require.False(t, publicBackendPort("area", 8083, services))
	require.True(t, publicBackendPort("area", "graphql", services))
	require.True(t, publicBackendPort("area", 8080, services))
}

func TestRenderedOperationsStayOffPublicRoutes(t *testing.T) {
	for _, profile := range []string{"default", "full", "telemetry", "ingest-only"} {
		for _, controller := range []string{"nginx", "gateway"} {
			t.Run(profile+"/"+controller, func(t *testing.T) {
				t.Parallel()
				manifest, err := renderChart(t, map[string]interface{}{
					"profile":       profile,
					"ingress":       map[string]interface{}{"enabled": true, "controller": controller},
					"networkPolicy": map[string]interface{}{"enabled": profile == "full"},
				})
				require.NoError(t, err)
				var docs []map[string]interface{}
				services := map[string]map[string]string{}
				serviceOps := 0
				for _, raw := range splitManifests(manifest) {
					var obj map[string]interface{}
					require.NoError(t, yaml.Unmarshal([]byte(raw), &obj))
					if len(obj) == 0 {
						continue
					}
					docs = append(docs, obj)
					if obj["kind"] != "Service" {
						continue
					}
					name := obj["metadata"].(map[string]interface{})["name"].(string)
					services[name] = map[string]string{}
					walkRendered("", obj["spec"], func(_ string, m map[string]interface{}) {
						if p, ok := m["port"]; ok {
							n := fmt.Sprint(m["name"])
							services[name][fmt.Sprint(p)] = n
							if n == "ops" {
								serviceOps++
								require.EqualValues(t, core.OpsPort, p)
								require.Equal(t, "ops", m["targetPort"])
							}
						}
					}, nil)
				}
				routes, probes, monitors, opsPorts, policies := 0, 0, 0, 0, 0
				for _, obj := range docs {
					kind := fmt.Sprint(obj["kind"])
					name := fmt.Sprint(obj["metadata"].(map[string]interface{})["name"])
					walkRendered("", obj, func(path string, m map[string]interface{}) {
						if kind == "Ingress" && strings.HasSuffix(path, ".backend.service") {
							routes++
							p := m["port"].(map[string]interface{})
							port := p["name"]
							if port == nil {
								port = p["number"]
							}
							require.True(t, publicBackendPort(fmt.Sprint(m["name"]), port, services), "%s routes to %v", name, port)
						}
						if kind == "HTTPRoute" && strings.Contains(path, ".backendRefs[") {
							routes++
							require.True(t, publicBackendPort(fmt.Sprint(m["name"]), m["port"], services), "%s routes to %v", name, m["port"])
						}
						if kind == "Deployment" && strings.HasSuffix(path, "Probe.httpGet") && (m["path"] == "/healthz" || m["path"] == "/readyz") {
							probes++
							require.Equal(t, "ops", m["port"], name)
						}
						if kind == "Deployment" && m["name"] == "ops" {
							opsPorts++
							require.EqualValues(t, core.OpsPort, m["containerPort"], name)
						}
						if kind == "ServiceMonitor" && strings.Contains(path, ".endpoints[") {
							monitors++
							require.Equal(t, "ops", m["port"], name)
						}
						if kind == "NetworkPolicy" && strings.HasSuffix(name, "-user-management-internal-auth") && strings.Contains(path, ".ingress[") {
							if ports, ok := m["ports"].([]interface{}); ok {
								for _, raw := range ports {
									port := raw.(map[string]interface{})
									if fmt.Sprint(port["port"]) == fmt.Sprint(core.OpsPort) {
										require.NotContains(t, m, "from", "ops must admit the scraper just like the public port")
									}
								}
							}
							if fmt.Sprint(m["port"]) == fmt.Sprint(core.OpsPort) {
								policies++
							}
						}
					}, nil)
				}
				require.Positive(t, routes)
				require.Positive(t, opsPorts)
				require.Equal(t, opsPorts, serviceOps)
				require.Equal(t, opsPorts*3, probes)
				require.Equal(t, opsPorts, monitors)
				if profile == "full" {
					require.Positive(t, policies, "user-management ops ingress missing")
				}
			})
		}
	}
}
