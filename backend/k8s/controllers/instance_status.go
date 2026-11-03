// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"fmt"
	"sort"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
)

// The labels the chart stamps on every functional-area Deployment
// (devicechain.areaLabels): the instance id, and the area the Deployment serves.
const (
	areaLabel     = "devicechain.io/functional-area"
	instanceLabel = corev1beta1.InstanceNamespaceLabel

	// maxNamed bounds how many areas a condition message names, so a message stays
	// short on an instance with every area down.
	maxNamed = 5

	// certWarnWindow is how close to expiry the broker's certificate is reported as
	// expiring soon. It equals what dcctl upgrade renews inside (natsLeafRenewBefore),
	// and the chart's BrokerCertificateExpiringSoon alert fires on the same boundary,
	// so a warning here and one in the alert manager arrive together.
	certWarnWindow = 30 * 24 * time.Hour

	// certRecheck bounds how long the operator goes without looking at a published
	// expiry again. The boundaries are scheduled exactly (see certificateVerdict); this
	// is the backstop, so a missed timer or a stepped clock is corrected within the hour.
	certRecheck = time.Hour
)

// instanceForDeployment maps a Deployment back to the Instance that owns it, or
// "" when it is not one of an instance's functional-area workloads. Both the label
// and the namespace must agree, so a Deployment carrying the label in some other
// namespace cannot move an instance's status.
func instanceForDeployment(d *appsv1.Deployment) string {
	id := d.Labels[instanceLabel]
	if id == "" || d.Labels[areaLabel] == "" || d.Namespace != corev1beta1.InstanceNamespace(id) {
		return ""
	}
	return id
}

// computeStatus derives the whole observed status from the instance and the
// Deployments found for it. It is a pure function of its arguments: no clock is
// read here (SetStatusCondition stamps transition times only when a condition's
// status actually changes), so the same inputs always give the same status and
// the reconciler can skip the write when nothing moved.
//
// now is a parameter for the same reason: time passing is an input like any other, and
// it is what flips BrokerCertificateValid with no event to announce it.
func computeStatus(instance *corev1beta1.Instance, deployments []appsv1.Deployment, now time.Time) corev1beta1.InstanceStatus {
	areas := make([]corev1beta1.AreaStatus, 0, len(deployments))
	byName := make(map[string]*appsv1.Deployment, len(deployments))
	for i := range deployments {
		d := &deployments[i]
		name := d.Labels[areaLabel]
		if name == "" {
			continue
		}
		byName[name] = d
		areas = append(areas, corev1beta1.AreaStatus{
			Name:              name,
			DesiredReplicas:   desiredReplicas(d),
			ReadyReplicas:     d.Status.ReadyReplicas,
			AvailableReplicas: d.Status.AvailableReplicas,
			UpdatedReplicas:   d.Status.UpdatedReplicas,
			Image:             mainImage(d),
		})
	}
	sort.Slice(areas, func(i, j int) bool { return areas[i].Name < areas[j].Name })

	running := runningVersion(areas)
	status := corev1beta1.InstanceStatus{
		ObservedGeneration: instance.Generation,
		// Conditions start from what is already recorded so that SetStatusCondition keeps
		// lastTransitionTime when a condition's STATUS has not moved.
		Conditions: append([]metav1.Condition(nil), instance.Status.Conditions...),
		Areas:      areas,
		Version:    &corev1beta1.ObservedVersion{Requested: instance.Spec.ImageVersion, Running: running},
	}

	available := workloadsAvailable(areas, byName)
	rollout := rolloutComplete(instance.Spec.ImageVersion, running, areas, byName)
	cert, _ := certificateVerdict(instance, now)
	ready := summarize(available, rollout, cert)

	for _, c := range []metav1.Condition{
		{Type: corev1beta1.ConditionReady, Status: ready.Status, Reason: ready.Reason, Message: ready.Message},
		{Type: corev1beta1.ConditionWorkloadsAvailable, Status: available.Status, Reason: available.Reason, Message: available.Message},
		{Type: corev1beta1.ConditionRolloutComplete, Status: rollout.Status, Reason: rollout.Reason, Message: rollout.Message},
		{Type: corev1beta1.ConditionBrokerCertificateValid, Status: cert.Status, Reason: cert.Reason, Message: cert.Message},
	} {
		c.ObservedGeneration = instance.Generation
		meta.SetStatusCondition(&status.Conditions, c)
	}
	return status
}

func desiredReplicas(d *appsv1.Deployment) int32 {
	if d.Spec.Replicas == nil {
		return 1 // the API server's default
	}
	return *d.Spec.Replicas
}

// mainImage is the first container's image: the chart renders the service as the
// first container and sidecars after it.
func mainImage(d *appsv1.Deployment) string {
	if cs := d.Spec.Template.Spec.Containers; len(cs) > 0 {
		return cs[0].Image
	}
	return ""
}

// imageTag extracts the tag from an image reference, or "" for one that has none
// (untagged, or pinned by digest alone, which names no version).
func imageTag(image string) string {
	if i := strings.Index(image, "@"); i >= 0 {
		image = image[:i] // a tag beside a digest is still the tag; digest alone leaves ""
	}
	slash := strings.LastIndex(image, "/")
	colon := strings.LastIndex(image, ":")
	if colon < 0 || colon < slash {
		return ""
	}
	return image[colon+1:]
}

// runningVersion is the single tag every area wanting pods runs, VersionMixed when
// they differ, and "" when it cannot be derived. Areas scaled to zero run nothing
// and say nothing about the version.
func runningVersion(areas []corev1beta1.AreaStatus) string {
	tag := ""
	seen := false
	for _, a := range areas {
		if a.DesiredReplicas == 0 {
			continue
		}
		t := imageTag(a.Image)
		if t == "" {
			return ""
		}
		if !seen {
			tag, seen = t, true
		} else if t != tag {
			return corev1beta1.VersionMixed
		}
	}
	return tag
}

// differing names the areas whose tag is not the requested one, with the tag each runs.
func differing(requested, running string, areas []corev1beta1.AreaStatus) string {
	var out []string
	for _, a := range areas {
		if a.DesiredReplicas == 0 {
			continue
		}
		if t := imageTag(a.Image); !sameVersion(requested, t) {
			out = append(out, fmt.Sprintf("%s runs %q", a.Name, t))
		}
	}
	if len(out) == 0 {
		return fmt.Sprintf("the workloads run %q", running)
	}
	return named(out)
}

func named(names []string) string {
	if len(names) > maxNamed {
		return strings.Join(names[:maxNamed], ", ") + fmt.Sprintf(" and %d more", len(names)-maxNamed)
	}
	return strings.Join(names, ", ")
}

type verdict struct {
	Status  metav1.ConditionStatus
	Reason  string
	Message string
}

func noWorkloads() verdict {
	return verdict{metav1.ConditionFalse, corev1beta1.ReasonNoWorkloads,
		"no functional-area Deployments found in the instance namespace: they have not been rendered yet, or were removed"}
}

// workloadsAvailable: True only when every area that wants pods has all of them
// available. A scaled-to-zero area wants none and does not count against it, but an
// instance whose areas are ALL scaled to zero serves nothing and is not available.
func workloadsAvailable(areas []corev1beta1.AreaStatus, byName map[string]*appsv1.Deployment) verdict {
	if len(areas) == 0 {
		return noWorkloads()
	}
	var none, short []string
	wanting := 0
	for _, a := range areas {
		if a.DesiredReplicas == 0 {
			continue
		}
		wanting++
		avail := a.AvailableReplicas
		switch {
		case avail == 0:
			none = append(none, a.Name)
		case avail < a.DesiredReplicas:
			short = append(short, fmt.Sprintf("%s (%d/%d)", a.Name, avail, a.DesiredReplicas))
		}
	}
	switch {
	case wanting == 0:
		return verdict{metav1.ConditionFalse, corev1beta1.ReasonNoWorkloads,
			"every functional area is scaled to zero replicas"}
	case len(none) > 0:
		return verdict{metav1.ConditionFalse, corev1beta1.ReasonAreaUnavailable,
			fmt.Sprintf("no pod available for: %s", named(none))}
	case len(short) > 0:
		return verdict{metav1.ConditionFalse, corev1beta1.ReasonAreaDegraded,
			fmt.Sprintf("fewer pods available than wanted: %s", named(short))}
	}
	return verdict{metav1.ConditionTrue, corev1beta1.ReasonAllAreasAvailable,
		fmt.Sprintf("%d areas have every wanted pod available", wanting)}
}

// rolloutComplete: True when every Deployment has been observed at its current
// generation and holds exactly its wanted replicas, all updated, and the running
// version is the requested one.
//
// 🔴 IT IS NOT "available >= desired". During a rolling update old pods count as
// available, so that test reads true with the new template not yet running. The
// test is generation observed AND updated == desired AND total == updated.
func rolloutComplete(requested, running string, areas []corev1beta1.AreaStatus, byName map[string]*appsv1.Deployment) verdict {
	if len(areas) == 0 {
		return noWorkloads()
	}
	var stalled, pending, short []string
	for _, a := range areas {
		d := byName[a.Name]
		for _, c := range d.Status.Conditions {
			if c.Type == appsv1.DeploymentProgressing && c.Status == corev1.ConditionFalse {
				stalled = append(stalled, a.Name)
			}
		}
		switch {
		case d.Status.ObservedGeneration < d.Generation ||
			d.Status.UpdatedReplicas != a.DesiredReplicas ||
			d.Status.Replicas != a.DesiredReplicas:
			pending = append(pending, a.Name)
		case a.AvailableReplicas != a.DesiredReplicas:
			// Converged on the template, but pods are missing or not ready: a crashed or
			// unschedulable pod, not an unfinished rollout. Kubernetes counts this as
			// not complete too, so RolloutComplete stays False, with its own message.
			short = append(short, a.Name)
		}
	}
	switch {
	case len(stalled) > 0:
		return verdict{metav1.ConditionFalse, corev1beta1.ReasonProgressDeadlineExceeded,
			fmt.Sprintf("rollout has stopped progressing for: %s", named(stalled))}
	case len(pending) > 0:
		return verdict{metav1.ConditionFalse, corev1beta1.ReasonRolloutInProgress,
			fmt.Sprintf("not yet converged on the current template: %s", named(pending))}
	case len(short) > 0:
		return verdict{metav1.ConditionFalse, corev1beta1.ReasonRolloutInProgress,
			fmt.Sprintf("template rolled out, but pods are not all available: %s (see WorkloadsAvailable)", named(short))}
	case running == "":
		return verdict{metav1.ConditionUnknown, corev1beta1.ReasonVersionUnknown,
			"the running version cannot be derived from the workloads' images"}
	case requested != "" && !sameVersion(requested, running):
		return verdict{metav1.ConditionFalse, corev1beta1.ReasonVersionMismatch,
			fmt.Sprintf("the instance requests %q but %s", requested, differing(requested, running, areas))}
	}
	return verdict{metav1.ConditionTrue, corev1beta1.ReasonRolloutComplete,
		fmt.Sprintf("all areas converged on %s", running)}
}

// sameVersion compares tags tolerating a leading "v" on either side.
func sameVersion(a, b string) bool {
	return strings.TrimPrefix(a, "v") == strings.TrimPrefix(b, "v")
}

// certificateVerdict reads the expiry dcctl published for the broker's certificate and
// its authority and says what it means at now, plus when that answer next changes
// (zero when nothing is published, so nothing needs a timer).
//
// 🔴 THE EXPIRY IS READ FROM AN ANNOTATION BECAUSE THE OPERATOR HAS NO ACCESS TO
// SECRETS, and a date is not a secret. What the operator contributes is the comparison
// with ITS clock, which is why this is evaluated on a timer rather than on events: no
// object changes when a certificate crosses a boundary.
//
// Unknown is returned for "nothing published" and for "published but unreadable", never
// a plausible Valid: an instance last touched by an older dcctl must not read as
// verified. When both leaf and authority are published the worse of the two is reported.
func certificateVerdict(instance *corev1beta1.Instance, now time.Time) (verdict, time.Duration) {
	type subject struct{ what, annotation string }
	subjects := []subject{
		{"broker certificate", corev1beta1.AnnotationBrokerCertificateNotAfter},
		{"broker certificate authority", corev1beta1.AnnotationBrokerCANotAfter},
	}
	var (
		expired, soon []string
		next          time.Duration
		published     int
		earliest      time.Time
	)
	schedule := func(d time.Duration) {
		if d > 0 && (next == 0 || d < next) {
			next = d
		}
	}
	for _, sub := range subjects {
		raw, ok := instance.Annotations[sub.annotation]
		if !ok {
			continue
		}
		notAfter, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return verdict{metav1.ConditionUnknown, corev1beta1.ReasonCertificateUnreadable,
				fmt.Sprintf("annotation %s is %q, which is not an RFC 3339 time; run dcctl upgrade to publish it again",
					sub.annotation, raw)}, 0
		}
		published++
		notAfter = notAfter.UTC()
		if earliest.IsZero() || notAfter.Before(earliest) {
			earliest = notAfter
		}
		switch remaining := notAfter.Sub(now); {
		case remaining <= 0:
			expired = append(expired, fmt.Sprintf("the %s expired at %s", sub.what, notAfter.Format(time.RFC3339)))
		case remaining <= certWarnWindow:
			soon = append(soon, fmt.Sprintf("the %s expires at %s", sub.what, notAfter.Format(time.RFC3339)))
			schedule(remaining + time.Second)
		default:
			schedule(remaining - certWarnWindow + time.Second)
		}
	}
	switch {
	case published == 0:
		return verdict{metav1.ConditionUnknown, corev1beta1.ReasonCertificateNotPublished,
			"no broker certificate expiry has been published for this instance; dcctl upgrade publishes it"}, 0
	case len(expired) > 0:
		return verdict{metav1.ConditionFalse, corev1beta1.ReasonCertificateExpired,
			strings.Join(expired, "; ") + ": the broker refuses TLS connections until it is renewed with dcctl upgrade"}, certRecheck
	case len(soon) > 0:
		return verdict{metav1.ConditionTrue, corev1beta1.ReasonCertificateExpiringSoon,
			strings.Join(soon, "; ") + ": run dcctl upgrade to renew it"}, capRecheck(next)
	}
	return verdict{metav1.ConditionTrue, corev1beta1.ReasonCertificateValid,
		fmt.Sprintf("the broker certificate is valid until %s", earliest.Format(time.RFC3339))}, capRecheck(next)
}

// capRecheck is d bounded by certRecheck: a published expiry is looked at again within
// the bound whatever the next boundary is.
func capRecheck(d time.Duration) time.Duration {
	if d <= 0 || d > certRecheck {
		return certRecheck
	}
	return d
}

// summarize folds the conditions into Ready: True only when workloads are available
// and the rollout is complete, else the first one that is not, with its reason. The
// certificate counts only when it is definitely False (expired): expiring soon and
// unknown do not make an otherwise serving instance unready.
func summarize(available, rollout, cert verdict) verdict {
	for _, v := range []verdict{available, rollout} {
		if v.Status != metav1.ConditionTrue {
			return v
		}
	}
	if cert.Status == metav1.ConditionFalse {
		return cert
	}
	return verdict{metav1.ConditionTrue, corev1beta1.ReasonReady,
		"every area is available and the rollout is complete"}
}

// CacheOptions is the manager cache configuration the status controller needs: only
// instance-labelled Deployments are cached, so the operator does not hold every
// Deployment in the cluster in memory. main.go and the envtest rig both use it.
func CacheOptions() cache.Options {
	return cache.Options{ByObject: map[client.Object]cache.ByObject{
		&appsv1.Deployment{}: {Label: InstanceDeploymentSelector()},
	}}
}

// InstanceDeploymentSelector selects the Deployments the operator cares about:
// those carrying the instance label. The manager's cache is restricted to it so
// the operator does not hold every Deployment in the cluster in memory.
func InstanceDeploymentSelector() labels.Selector {
	req, err := labels.NewRequirement(instanceLabel, selection.Exists, nil)
	if err != nil {
		panic(err) // a constant key and operator: cannot fail
	}
	return labels.NewSelector().Add(*req)
}
