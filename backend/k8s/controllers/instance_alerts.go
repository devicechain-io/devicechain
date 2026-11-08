// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	corev1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
)

// alertRoutingRemedy is what a reader does about an instance whose alerts go nowhere.
const alertRoutingRemedy = "run dcctl install (or dcctl upgrade) with --alert-webhook, --alert-email or --alert-config"

// alertRoutingVerdict reports whether the alerts the instance's rules raise reach a human.
//
// 🔴 ONLY A PUBLISHED, READABLE ROUTING EVER READS AS ROUTED. The operator has no access
// to the Alertmanager's configuration Secret, so it reads the summary dcctl published
// (AnnotationAlertRouting). Absent or unreadable is Unknown, never True: an instance last
// touched by a dcctl from before alert routing existed must not look verified.
//
// With monitoring off no rule is rendered, so there is nothing to route and the answer is
// Unknown rather than the False that would tell an operator to configure a receiver for
// a stack they chose not to run.
func alertRoutingVerdict(instance *corev1beta1.Instance) verdict {
	if !instance.Spec.Monitoring {
		return verdict{metav1.ConditionUnknown, corev1beta1.ReasonMonitoringDisabled,
			"the instance runs no monitoring stack, so no alert rules are rendered and there is nothing to route"}
	}
	raw, ok := instance.Annotations[corev1beta1.AnnotationAlertRouting]
	if !ok {
		return verdict{metav1.ConditionUnknown, corev1beta1.ReasonAlertRoutingNotPublished,
			"no alert routing has been published for this instance; dcctl upgrade publishes it"}
	}
	routing, err := corev1beta1.ParseAlertRouting(raw)
	if err != nil {
		return verdict{metav1.ConditionUnknown, corev1beta1.ReasonAlertRoutingUnreadable,
			fmt.Sprintf("annotation %s is unreadable (%v); run dcctl upgrade to publish it again",
				corev1beta1.AnnotationAlertRouting, err)}
	}
	if !routing.Routed() {
		return verdict{metav1.ConditionFalse, corev1beta1.ReasonAlertsNotRouted,
			"ALERTS NOT ROUTED: the alerting pipeline has no receiver, so every alert is dropped and nobody is told when " +
				"something breaks; " + alertRoutingRemedy}
	}
	msg := fmt.Sprintf("alerts are routed to: %s", strings.Join(routing.Receivers, ", "))
	if !routing.Watchdog {
		msg += "; the Watchdog dead-man alert is not routed, so a silent alerting pipeline would go unnoticed (--alert-watchdog-webhook)"
	}
	return verdict{metav1.ConditionTrue, corev1beta1.ReasonAlertsRouted, msg}
}
