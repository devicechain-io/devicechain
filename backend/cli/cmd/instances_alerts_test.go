// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"strings"
	"testing"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
)

func declaredWith(monitoring bool, routing string) *dcv1beta1.Instance {
	i := &dcv1beta1.Instance{}
	i.Spec.Monitoring = monitoring
	i.Annotations = map[string]string{dcv1beta1.AnnotationPhase: dcv1beta1.PhaseReady}
	if routing != "" {
		i.Annotations[dcv1beta1.AnnotationAlertRouting] = routing
	}
	return i
}

// The row warns only about a routing that was published and says nothing reaches a person.
func TestTheInstanceRowWarnsOnlyOfPublishedUnroutedAlerts(t *testing.T) {
	for _, c := range []struct {
		name string
		inst *dcv1beta1.Instance
		warn bool
	}{
		{"none", declaredWith(true, "none"), true},
		{"a watchdog alone", declaredWith(true, "none,watchdog"), true},
		{"a webhook", declaredWith(true, "webhook"), false},
		{"nothing published", declaredWith(true, ""), false},
		{"unreadable", declaredWith(true, "pagerduty"), false},
		{"no monitoring", declaredWith(false, "none"), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := declarationPhaseWords(c.inst) + alertRoutingWarning(c.inst)
			if !strings.HasPrefix(got, "declared ready") {
				t.Fatalf("the phase words moved: %q", got)
			}
			if has := strings.Contains(got, "ALERTS NOT ROUTED"); has != c.warn {
				t.Fatalf("row %q: warning present = %v, want %v", got, has, c.warn)
			}
		})
	}
}
