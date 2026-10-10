// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package governance

import (
	core "github.com/devicechain-io/dc-microservice/core"
	"github.com/prometheus/client_golang/prometheus"
)

// The safety gates that switch themselves OFF when their configuration is absent. Each is a
// deliberate availability trade (the device plane must not hard-depend on the authority the gate
// consults), logged at WARN when it is off, and invisible in every other way: work keeps flowing
// and the losses the gate exists to prevent resume without a single error. The gauge below is
// what makes "off" something an alert can see.
const (
	// GateTenantLifecycle refuses work for a tenant that has been deleted.
	GateTenantLifecycle = "tenant_lifecycle"
	// GatePresence withholds commands for devices a transport reports as absent.
	GatePresence = "presence"
	// GateRuleValidation compiles a profile's detection rules against event-processing at publish.
	GateRuleValidation = "rule_validation"
)

// SafetyGates reports, per gate, whether a service's optional safety gate is wired.
type SafetyGates struct{ vec *prometheus.GaugeVec }

// NewSafetyGates registers <area>_safety_gate_enabled{gate} (1 when the gate is wired, 0 when it
// is off).
//
// 🔴 CALL IT ONCE, FROM THE INITIALIZE PHASE, AND PASS THE RESULT TO WHATEVER DECIDES A GATE. It
// registers on construction and a second call for the same service panics on the duplicate
// collector, which is the point: the gates are decided in callbacks that run again on a restart,
// and a gauge built there would panic on the second start. (An earlier version swallowed the
// duplicate and returned the existing gauge, which hid exactly that mistake.)
func NewSafetyGates(ms *core.Microservice) *SafetyGates {
	return &SafetyGates{vec: ms.NewGaugeVec("safety_gate_enabled",
		"1 when an optional safety gate is wired in this service, 0 when it is OFF because its "+
			"configuration is absent. An OFF gate lets work through that the gate exists to refuse "+
			"(tenant_lifecycle: work for a deleted tenant; presence: commands to absent devices; "+
			"rule_validation: uncompilable detection rules at profile publish).",
		[]string{"gate"})}
}

// Set records whether a gate is wired. A nil receiver is a no-op, so a caller built without
// metrics (a test fixture) runs unmeasured.
func (g *SafetyGates) Set(gate string, enabled bool) {
	if g == nil {
		return
	}
	v := 0.0
	if enabled {
		v = 1.0
	}
	g.vec.WithLabelValues(gate).Set(v)
}
