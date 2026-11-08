// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	corev1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
)

func instanceWithRouting(monitoring bool, value string, published bool) *corev1beta1.Instance {
	i := &corev1beta1.Instance{ObjectMeta: metav1.ObjectMeta{Name: "x", Annotations: map[string]string{}}}
	i.Spec.Monitoring = monitoring
	if published {
		i.Annotations[corev1beta1.AnnotationAlertRouting] = value
	}
	return i
}

func TestAlertRoutingVerdict(t *testing.T) {
	for _, c := range []struct {
		name   string
		inst   *corev1beta1.Instance
		status metav1.ConditionStatus
		reason string
		in     string
	}{
		{"monitoring off is not a warning", instanceWithRouting(false, "none", true), metav1.ConditionUnknown, corev1beta1.ReasonMonitoringDisabled, ""},
		{"nothing published is Unknown, never routed", instanceWithRouting(true, "", false), metav1.ConditionUnknown, corev1beta1.ReasonAlertRoutingNotPublished, ""},
		{"an unreadable value is Unknown, never routed", instanceWithRouting(true, "pagerduty", true), metav1.ConditionUnknown, corev1beta1.ReasonAlertRoutingUnreadable, "pagerduty"},
		{"none is the warning", instanceWithRouting(true, "none", true), metav1.ConditionFalse, corev1beta1.ReasonAlertsNotRouted, "ALERTS NOT ROUTED"},
		{"a watchdog alone is still the warning", instanceWithRouting(true, "none,watchdog", true), metav1.ConditionFalse, corev1beta1.ReasonAlertsNotRouted, "ALERTS NOT ROUTED"},
		{"a webhook routes", instanceWithRouting(true, "webhook,watchdog", true), metav1.ConditionTrue, corev1beta1.ReasonAlertsRouted, "webhook"},
		{"routed without a watchdog says so", instanceWithRouting(true, "email", true), metav1.ConditionTrue, corev1beta1.ReasonAlertsRouted, "Watchdog"},
	} {
		t.Run(c.name, func(t *testing.T) {
			v := alertRoutingVerdict(c.inst)
			if v.Status != c.status || v.Reason != c.reason {
				t.Fatalf("got %s/%s, want %s/%s (%s)", v.Status, v.Reason, c.status, c.reason, v.Message)
			}
			if !strings.Contains(v.Message, c.in) {
				t.Errorf("message %q does not contain %q", v.Message, c.in)
			}
		})
	}
}

// The warning must never gate Ready: an instance that serves is ready whether or not
// anyone would be told if it stopped.
func TestAlertsNotRoutedDoesNotGateReady(t *testing.T) {
	inst := instanceWithRouting(true, "none", true)
	status := computeStatus(inst, nil, certNow)
	var routed, ready *metav1.Condition
	for i := range status.Conditions {
		switch status.Conditions[i].Type {
		case corev1beta1.ConditionAlertsRouted:
			routed = &status.Conditions[i]
		case corev1beta1.ConditionReady:
			ready = &status.Conditions[i]
		}
	}
	if routed == nil || routed.Status != metav1.ConditionFalse {
		t.Fatalf("AlertsRouted = %+v, want False", routed)
	}
	// With no workloads Ready is False for ITS reason, never for the alerts.
	if ready == nil || ready.Reason == corev1beta1.ReasonAlertsNotRouted {
		t.Fatalf("Ready took the alert warning's reason: %+v", ready)
	}
}
