// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package governance

import (
	"errors"

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
// is off). It is safe to call more than once for the same service: the second call returns the
// gauge the first registered, so a caller inside a lifecycle callback that can be entered again
// cannot panic on a duplicate registration.
func NewSafetyGates(ms *core.Microservice) *SafetyGates {
	vec := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: core.METRICS_NAMESPACE,
		Subsystem: ms.MetricsSubsystem(),
		Name:      "safety_gate_enabled",
		Help: "1 when an optional safety gate is wired in this service, 0 when it is OFF because its " +
			"configuration is absent. An OFF gate lets work through that the gate exists to refuse " +
			"(tenant_lifecycle: work for a deleted tenant; presence: commands to absent devices; " +
			"rule_validation: uncompilable detection rules at profile publish).",
	}, []string{"gate"})
	if reg := ms.MetricsRegisterer(); reg != nil {
		if err := reg.Register(vec); err != nil {
			var already prometheus.AlreadyRegisteredError
			if !errors.As(err, &already) {
				panic(err)
			}
			vec = already.ExistingCollector.(*prometheus.GaugeVec)
		}
	}
	return &SafetyGates{vec: vec}
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
