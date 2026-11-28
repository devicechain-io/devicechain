// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"github.com/devicechain-io/dc-microservice/service"
	"go/ast"
	"go/parser"
	"go/token"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/devicechain-io/dc-command-delivery/model"
	"github.com/devicechain-io/dc-microservice/auth"
	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/governance"
	"github.com/devicechain-io/dc-microservice/svcclient"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gateReading builds the safety-gate gauge the way the service does (once, on a registry) and
// returns the handle with a reader for one gate's value. present=false is a different answer
// from 0: an absent series is a gate nobody reported, which no alert can see.
func gateReading(t *testing.T, area string) (*governance.SafetyGates, func(gate string) (float64, bool)) {
	t.Helper()
	ms := &core.Microservice{InstanceId: "test", FunctionalArea: area}
	reg := prometheus.NewRegistry()
	ms.UseMetricsRegistry(reg)
	gates := service.FromManagers(ms, service.Managers{}).SafetyGates()
	return gates, func(gate string) (float64, bool) {
		families, err := reg.Gather()
		require.NoError(t, err)
		for _, f := range families {
			for _, m := range f.GetMetric() {
				for _, l := range m.GetLabel() {
					if l.GetName() == "gate" && l.GetValue() == gate {
						return m.GetGauge().GetValue(), true
					}
				}
			}
		}
		return 0, false
	}
}

func fullInfra() mscfg.InfrastructureConfiguration {
	var infra mscfg.InfrastructureConfiguration
	infra.ServiceAuth.Secret = "s3cret"
	infra.UserManagement.Hostname, infra.UserManagement.Port = "user-management", 8080
	infra.UserManagement.InternalPort = 8080
	infra.DeviceState.Hostname, infra.DeviceState.Port = "device-state", 8080
	infra.EventProcessing.Hostname, infra.EventProcessing.Port = "event-processing", 8080
	return infra
}

// The presence gate is OFF (nil reader, gauge 0) when the secret or the device-state endpoint is
// absent, and ON (reader, gauge 1) when both are set. Each missing coordinate is tried alone, and
// the decision must be recorded on the presence gate and on no other.
func TestPresenceReaderRecordsItsDecision(t *testing.T) {
	for name, c := range map[string]struct {
		mutate  func(*mscfg.InfrastructureConfiguration)
		missing string
	}{
		"no secret": {func(i *mscfg.InfrastructureConfiguration) { i.ServiceAuth.Secret = "" },
			"infrastructure.serviceAuth.secret"},
		"no hostname": {func(i *mscfg.InfrastructureConfiguration) { i.DeviceState.Hostname = "" },
			"infrastructure.deviceState.hostname"},
		"no port": {func(i *mscfg.InfrastructureConfiguration) { i.DeviceState.Port = 0 },
			"infrastructure.deviceState.port"},
		// A document predating the deviceState block: both of its keys are named, so one
		// restart fixes it.
		"no deviceState block": {func(i *mscfg.InfrastructureConfiguration) {
			i.DeviceState.Hostname, i.DeviceState.Port = "", 0
		}, "infrastructure.deviceState.hostname, infrastructure.deviceState.port"},
		"nothing": {func(i *mscfg.InfrastructureConfiguration) {
			i.ServiceAuth.Secret, i.DeviceState.Hostname, i.DeviceState.Port = "", "", 0
		}, "infrastructure.serviceAuth.secret, infrastructure.deviceState.hostname, infrastructure.deviceState.port"},
	} {
		gates, read := gateReading(t, "command-delivery")
		infra := fullInfra()
		c.mutate(&infra)
		reader, missing := presenceReader(infra, svcclient.NewPeers(infra, "command-delivery"), gates)
		assert.Nil(t, reader, name)
		assert.Equal(t, c.missing, missing, "%s: the OFF gate must name exactly what is missing", name)
		v, ok := read(governance.GatePresence)
		assert.True(t, ok, "%s: an OFF gate must be a present series", name)
		assert.Equal(t, 0.0, v, name)
	}

	gates, read := gateReading(t, "command-delivery")
	reader, missing := presenceReader(fullInfra(), svcclient.NewPeers(fullInfra(), "command-delivery"), gates)
	assert.NotNil(t, reader)
	assert.Empty(t, missing, "a wired gate names nothing missing")
	v, ok := read(governance.GatePresence)
	assert.True(t, ok)
	assert.Equal(t, 1.0, v)
	_, other := read(governance.GateRuleValidation)
	assert.False(t, other, "the presence decision must not be recorded on another gate")
}

// The gate decisions this service makes, by function name.
var decisions = []string{"tenantLifecycleGate", "presenceReader"}

// callSitesPassTheServiceGates is a SOURCE-LEVEL check, chosen because afterMicroserviceInitialized
// cannot run in a unit test (it needs the whole microservice, a database and a broker). It parses
// main.go and asserts three things the helper tests cannot see: the service gates are obtained exactly once, in
// afterMicroserviceInitialized, into safetyGates; every call to a gate decision passes exactly
// safetyGates (a nil, or a handle built somewhere else, would leave the series absent and the
// SafetyGateDisabled alert blind); and each decision is called at least once (so a renamed or
// removed call site cannot pass vacuously).
func TestEveryGateDecisionIsCalledWithTheServiceGates(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "main.go", nil, 0)
	require.NoError(t, err)

	calls := map[string]int{}
	built := 0
	ast.Inspect(file, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			return true
		}
		ast.Inspect(fn.Body, func(m ast.Node) bool {
			call, ok := m.(*ast.CallExpr)
			if !ok {
				return true
			}
			name := ""
			switch f := call.Fun.(type) {
			case *ast.Ident:
				name = f.Name
			case *ast.SelectorExpr:
				name = f.Sel.Name
			}
			if name == "SafetyGates" {
				sel, ok := call.Fun.(*ast.SelectorExpr)
				require.True(t, ok)
				id, ok := sel.X.(*ast.Ident)
				assert.True(t, ok && id.Name == "Svc", "gates must come from Svc")
				built++
				assert.Equal(t, "afterMicroserviceInitialized", fn.Name.Name,
					"the gauge must be built on the initialize path, once")
			}
			for _, want := range decisions {
				if name != want {
					continue
				}
				calls[name]++
				last := call.Args[len(call.Args)-1]
				id, isIdent := last.(*ast.Ident)
				assert.Truef(t, isIdent && id.Name == "safetyGates",
					"%s is called with %T %v as its gates; it must be safetyGates", name, last, last)
			}
			return true
		})
		return false
	})
	assert.Equal(t, 1, built, "Svc.SafetyGates must be called exactly once")
	for _, want := range decisions {
		assert.GreaterOrEqualf(t, calls[want], 1, "no call to %s found: renamed, or no longer wired", want)
	}
}

func TestTenantLifecycleGateRecordsItsDecision(t *testing.T) {
	infra := fullInfra()
	infra.UserManagement.Hostname = ""
	gates, read := gateReading(t, "command-delivery")
	assert.Nil(t, tenantLifecycleGate(svcclient.NewPeers(infra, "command-delivery"), gates))
	v, ok := read(governance.GateTenantLifecycle)
	assert.True(t, ok, "an OFF gate must be a present series")
	assert.Equal(t, 0.0, v)

	gates, read = gateReading(t, "command-delivery")
	assert.NotNil(t, tenantLifecycleGate(svcclient.NewPeers(fullInfra(), "command-delivery"), gates))
	v, ok = read(governance.GateTenantLifecycle)
	assert.True(t, ok)
	assert.Equal(t, 1.0, v)
}

// Exercise the real wiring rather than a separately constructed verify client: solar planning
// must ask for its newer authority on its own credential, minted on the internal listener.
func TestSiteWiringMintsItsOwnAuthorities(t *testing.T) {
	requests := make(chan auth.ServiceTokenRequest, 1)
	mint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, auth.ServiceTokenPath, r.URL.Path)
		assert.Equal(t, "s3cret", r.Header.Get(auth.ServiceSecretHeader))
		var request auth.ServiceTokenRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		} else {
			requests <- request
		}
		// Like an old replica, refuse the new authority. No device query should follow.
		http.Error(w, "authority unavailable", http.StatusForbidden)
	}))
	defer mint.Close()
	addr := mint.Listener.Addr().(*net.TCPAddr)
	infra := fullInfra()
	infra.UserManagement.Hostname = addr.IP.String()
	infra.UserManagement.Port = 1 // The public listener must never receive the mint.
	infra.UserManagement.InternalPort = uint32(addr.Port)
	infra.DeviceManagement.Hostname, infra.DeviceManagement.Port = addr.IP.String(), uint32(addr.Port)
	prevApi := Api
	t.Cleanup(func() { Api = prevApi })
	Api = &model.Api{}
	ms := &core.Microservice{FunctionalArea: "command-delivery"}
	wireDeviceManagementGates(infra, svcclient.NewPeers(infra, ms.FunctionalArea))
	ctx := core.WithTenant(context.Background(), "tenant")
	_, err := Api.SiteResolver.DeviceSites(ctx, []string{"device"})
	require.Error(t, err)
	require.Len(t, requests, 1, "site resolver must mint its own credential")
	request := <-requests
	assert.Equal(t, ms.FunctionalArea, request.Subject)
	assert.Equal(t, []string{string(auth.DeviceRead), string(auth.LocationInstalledRead)}, request.Authorities)
}

// TestThePresenceGateOffReasonReachesTheProcessor is a SOURCE-LEVEL check for the same reason
// the one above is: the wiring runs only inside the NATS manager's construction callback. It
// asserts that the second result of presenceReader -- the missing keys -- is what main.go
// assigns to the processor's PresenceGateOff. Dropped, the gate would still read OFF on its
// gauge but every ungated dispatch would go uncounted and unwarned.
func TestThePresenceGateOffReasonReachesTheProcessor(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "main.go", nil, 0)
	require.NoError(t, err)

	reasons := map[string]bool{}
	assigned := 0
	ast.Inspect(file, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		if len(as.Rhs) == 1 && len(as.Lhs) == 2 {
			if call, ok := as.Rhs[0].(*ast.CallExpr); ok {
				if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "presenceReader" {
					if reason, ok := as.Lhs[1].(*ast.Ident); ok && reason.Name != "_" {
						reasons[reason.Name] = true
					}
				}
			}
		}
		for i, lhs := range as.Lhs {
			sel, ok := lhs.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "PresenceGateOff" || i >= len(as.Rhs) {
				continue
			}
			if rhs, ok := as.Rhs[i].(*ast.Ident); ok && reasons[rhs.Name] {
				assigned++
			}
		}
		return true
	})
	require.NotEmpty(t, reasons, "presenceReader's missing-keys result is discarded")
	assert.Equal(t, 1, assigned, "PresenceGateOff must be assigned presenceReader's missing-keys result, once")
}
