// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/fatih/color"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
)

// MQTT exposure: how devices OUTSIDE the cluster reach the platform's MQTT broker.
//
//	none          (the default) no new public port. An upgrade never opens one.
//	gateway       a TLSRoute in Passthrough mode on the instance Gateway, port 8883, routed by the
//	              SNI name --mqtt-host. The broker terminates TLS, never the ingress controller.
//	loadbalancer  a Service of type LoadBalancer for 8883 straight to the broker pods.
//
// 🔴 PLAINTEXT MQTT IS NEVER EXPOSED BY THIS FEATURE. Both modes carry the broker's own TLS
// listener, and a request made while TLS is off is refused here, in the chart, and by the
// declaration's own CEL rule.
const (
	MQTTExposureNone         = "none"
	MQTTExposureGateway      = "gateway"
	MQTTExposureLoadBalancer = "loadbalancer"
)

// mqttsPort is the port devices dial, in both modes.
const mqttsPort = 8883

// mqttHostPattern is the chart's own rule for the host (devicechain.mqttExposure), restated:
// a fully-qualified, lower-case DNS name. A bare label, an IP address or an upper-case name is
// refused, because devices verify the certificate against the name they dial and the Gateway
// matches it exactly.
var mqttHostPattern = regexp.MustCompile(`^([a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?\.)+[a-z]([-a-z0-9]{0,61}[a-z0-9])?$`)

// ValidateMQTTHost refuses a host name devices could not use.
func ValidateMQTTHost(host string) error {
	if len(host) > 253 || !mqttHostPattern.MatchString(host) {
		return fmt.Errorf("--mqtt-host %q is not a fully-qualified lower-case DNS name (for example mqtt.example.com). "+
			"Devices verify the broker's certificate against the name they dial, and the Gateway routes by it", host)
	}
	return nil
}

// ValidateMQTTExposure refuses a value --mqtt-exposure does not take. Empty is "keep what is recorded".
func ValidateMQTTExposure(mode string) error {
	switch mode {
	case "", MQTTExposureNone, MQTTExposureGateway, MQTTExposureLoadBalancer:
		return nil
	}
	return fmt.Errorf("--mqtt-exposure %q: want gateway, loadbalancer or none (or leave it off to keep what the instance records)", mode)
}

// mqttSettlement is the exposure a run renders.
type mqttSettlement struct {
	Mode    string // "" and none are the same answer: nothing exposed
	Host    string
	Changed bool // differs from what the declaration records
}

// settleMQTTExposure decides the exposure a run renders from what the declaration records and
// what the run was given. Pure, so every refusal is testable without a cluster.
//
// 🔴 ABSENT FLAGS KEEP WHAT IS RECORDED, so an upgrade that never mentions the feature neither
// opens a port nor closes one; "none" is the only way to close it, and it is explicit. And every
// path that leaves an exposure ON re-checks that TLS is on, so an instance cannot be left
// exposing MQTT by a later --no-tls.
func settleMQTTExposure(recMode, recHost, givenMode, givenHost string, noTLS bool) (mqttSettlement, error) {
	if err := ValidateMQTTExposure(givenMode); err != nil {
		return mqttSettlement{}, err
	}
	mode, host := recMode, recHost
	if mode == "" {
		mode = MQTTExposureNone
	}
	switch {
	case givenMode == MQTTExposureNone:
		if givenHost != "" {
			return mqttSettlement{}, fmt.Errorf("--mqtt-host names the host of an exposure, and --mqtt-exposure none turns it off; give one")
		}
		mode, host = MQTTExposureNone, ""
	case givenMode != "":
		mode = givenMode
		if givenHost != "" {
			host = givenHost
		}
	case givenHost != "":
		if mode == MQTTExposureNone {
			return mqttSettlement{}, fmt.Errorf("--mqtt-host without --mqtt-exposure: this instance does not expose MQTT. " +
				"Give --mqtt-exposure gateway or loadbalancer with it")
		}
		host = givenHost
	}
	if mode != MQTTExposureNone {
		if host == "" {
			return mqttSettlement{}, fmt.Errorf("--mqtt-exposure %s needs --mqtt-host: the DNS name devices dial, which the broker's "+
				"certificate must carry", mode)
		}
		if err := ValidateMQTTHost(host); err != nil {
			return mqttSettlement{}, err
		}
		if noTLS {
			return mqttSettlement{}, fmt.Errorf("--mqtt-exposure %s is refused while TLS is off (--no-tls): plaintext MQTT is never "+
				"exposed outside the cluster. Serve this instance over TLS, or leave MQTT unexposed", mode)
		}
	}
	rm := recMode
	if rm == "" {
		rm = MQTTExposureNone
	}
	return mqttSettlement{Mode: mode, Host: host, Changed: mode != rm || host != recHost}, nil
}

// mqttExposureValues is the chart's mqttExposure block, nil when nothing is exposed so the
// release carries no key at all and renders exactly as it did before the feature existed.
func mqttExposureValues(st *State) map[string]interface{} {
	if st.MQTTExposure == "" || st.MQTTExposure == MQTTExposureNone {
		return nil
	}
	return map[string]interface{}{"mode": st.MQTTExposure, "host": st.MQTTHost}
}

// mqttBrokerNames are the extra names the broker's certificate must carry: the host devices dial.
func mqttBrokerNames(st *State) []string {
	if st.MQTTExposure == "" || st.MQTTExposure == MQTTExposureNone || st.MQTTHost == "" {
		return nil
	}
	return []string{st.MQTTHost}
}

// recordMQTTExposure writes the settled exposure onto a declaration spec. "none" is recorded as
// absent, so an instance that never exposed MQTT has a declaration byte-identical to before.
func recordMQTTExposure(spec *dcv1beta1.InstanceSpec, st *State) {
	if st.MQTTExposure == "" || st.MQTTExposure == MQTTExposureNone {
		spec.MQTTExposure, spec.MQTTHost = "", ""
		return
	}
	spec.MQTTExposure, spec.MQTTHost = st.MQTTExposure, st.MQTTHost
}

// mqttExposureRecorded says the declaration already holds what the State settled.
func mqttExposureRecorded(spec dcv1beta1.InstanceSpec, st *State) bool {
	var want dcv1beta1.InstanceSpec
	recordMQTTExposure(&want, st)
	return spec.MQTTExposure == want.MQTTExposure && spec.MQTTHost == want.MQTTHost
}

// adoptMQTTExposure settles the exposure into the State from the declaration (nil when there is
// none yet) and the run's flags.
func adoptMQTTExposure(st *State, recorded *dcv1beta1.InstanceSpec, givenMode, givenHost string) error {
	var recMode, recHost string
	if recorded != nil {
		recMode, recHost = recorded.MQTTExposure, recorded.MQTTHost
	}
	got, err := settleMQTTExposure(recMode, recHost, givenMode, givenHost, st.NoTLS)
	if err != nil {
		return err
	}
	st.MQTTExposure, st.MQTTHost, st.MQTTExposureChange = got.Mode, got.Host, got.Changed
	return nil
}

// refuseMQTTHostTaken refuses a host another instance on the cluster already exposes MQTT on.
// Routing is by SNI, so two instances answering to one name would race for each other's devices.
func refuseMQTTHostTaken(host, instance string, others []dcv1beta1.Instance) error {
	for i := range others {
		o := &others[i]
		if o.Name == instance || o.Spec.MQTTExposure == "" || o.Spec.MQTTExposure == MQTTExposureNone {
			continue
		}
		if strings.EqualFold(o.Spec.MQTTHost, host) {
			return fmt.Errorf("--mqtt-host %q is already the MQTT host of instance %q. Routing is by the name a device dials, "+
				"so every instance needs a host of its own (for example mqtt.%s.example.com)", host, o.Name, instance)
		}
	}
	return nil
}

// traefikServesMQTT says whether the cluster's Traefik Service publishes the 8883 entrypoint that
// gateway mode needs.
func traefikServesMQTT(ctx context.Context, typed kubernetes.Interface) (bool, error) {
	svcs, err := typed.CoreV1().Services(traefikNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/name=traefik,app.kubernetes.io/instance=traefik-" + traefikNamespace,
	})
	if err != nil {
		return false, fmt.Errorf("listing the Services in %s: %w", traefikNamespace, err)
	}
	for _, s := range svcs.Items {
		for _, p := range s.Spec.Ports {
			if p.Port == mqttsPort && p.Protocol != corev1.ProtocolUDP {
				return true, nil
			}
		}
	}
	return false, nil
}

// settleMQTTExposurePrereqs checks, against the cluster, what the settled exposure needs. It runs
// where a run is about to render and apply, never in a dry run.
func settleMQTTExposurePrereqs(ctx context.Context, st *State) error {
	if st.MQTTExposure == "" || st.MQTTExposure == MQTTExposureNone {
		return nil
	}
	if st.MQTTExposure == MQTTExposureGateway && st.IngressController != frontEndGateway {
		return fmt.Errorf("--mqtt-exposure gateway carries MQTT through the instance's Gateway, and this instance is served by %q. "+
			"Move it to the Gateway front end first (--ingress-controller gateway), or use --mqtt-exposure loadbalancer",
			st.IngressController)
	}
	dyn, _, typed, err := kubeClients(st.KubeContext)
	if err != nil {
		return fmt.Errorf("checking the cluster for --mqtt-exposure: %w", err)
	}
	if st.MQTTExposure == MQTTExposureGateway {
		ok, err := traefikServesMQTT(ctx, typed)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("--mqtt-exposure gateway needs the ingress controller to listen on %d, and this cluster's Traefik "+
				"does not. Open it once for the cluster with `dcctl install --mqtt-gateway` (it publishes a new port on the "+
				"controller's load balancer), or use --mqtt-exposure loadbalancer", mqttsPort)
		}
	}
	names, err := declaredInstances(ctx, dyn)
	if err != nil {
		return err
	}
	var others []dcv1beta1.Instance
	for _, n := range names {
		if n == st.Instance {
			continue
		}
		inst, err := readInstanceCR(ctx, dyn, n)
		if err != nil {
			return err
		}
		if inst != nil {
			others = append(others, *inst)
		}
	}
	return refuseMQTTHostTaken(st.MQTTHost, st.Instance, others)
}

// sayMQTTExposure reports the exposure in effect and what the operator must do next.
func sayMQTTExposure(st *State) {
	if st.MQTTExposure == "" || st.MQTTExposure == MQTTExposureNone {
		return
	}
	how := "a TLSRoute (Passthrough) on the instance Gateway"
	if st.MQTTExposure == MQTTExposureLoadBalancer {
		how = "a LoadBalancer Service"
	}
	fmt.Printf("  mqtt exposure    %s on port %d (%s), host %s\n", st.MQTTExposure, mqttsPort, how, st.MQTTHost)
	where := "the ingress controller's load balancer"
	if st.MQTTExposure == MQTTExposureLoadBalancer {
		where = "the " + natsReleaseName + "-mqtt-lb Service's load balancer"
	}
	fmt.Println(color.YellowString("  mqtt exposure    point DNS for %s at %s, and open TCP %d in the firewall; "+
		"the broker's certificate carries the host", st.MQTTHost, where, mqttsPort))
}

// recordedMQTTSpec is the exposure a State was hydrated with from the declaration (before this
// run's flags are applied), as a spec fragment.
func recordedMQTTSpec(st *State) dcv1beta1.InstanceSpec {
	return dcv1beta1.InstanceSpec{MQTTExposure: st.MQTTExposure, MQTTHost: st.MQTTHost}
}

// mqttGatewayPlan is the cluster root's Traefik MQTT entrypoint, and a warning when the run
// changes whether the controller's Service publishes 8883.
//
//	nil     keep what the controller's Service has now
//	true    open the 8883 entrypoint (a new port on the controller's load balancer)
//	false   close it
//
// 🔴 ABSENT MEANS KEEP, because the root's variable defaults to off: a re-run that forgot the flag
// would otherwise close the port every gateway-exposed instance's devices connect through.
func mqttGatewayPlan(ctx context.Context, typed kubernetes.Interface, given *bool) (vars, warnings []string, err error) {
	open, err := traefikServesMQTT(ctx, typed)
	if err != nil {
		return nil, nil, err
	}
	want := open
	if given != nil {
		want = *given
	}
	if want {
		vars = append(vars, "traefik_mqtt_entrypoint=true")
	}
	switch {
	case want && !open:
		warnings = append(warnings, fmt.Sprintf("--mqtt-gateway publishes TCP %d on the ingress controller's Service (on a cloud, "+
			"its load balancer). Nothing is routed to it until an instance asks: dcctl bootstrap|upgrade --mqtt-exposure gateway --mqtt-host <name>", mqttsPort))
	case !want && open:
		warnings = append(warnings, fmt.Sprintf("--mqtt-gateway=false closes TCP %d on the ingress controller. An instance exposed with "+
			"--mqtt-exposure gateway stops being reachable by devices until it is opened again", mqttsPort))
	}
	return vars, warnings, nil
}

// CheckMQTTExposureFlags judges --mqtt-exposure and --mqtt-host on their own, before any cluster
// exists. With no mode given there is nothing to judge here: a host alone is meaningful over an
// instance that already records an exposure, which only the pipeline can see.
func CheckMQTTExposureFlags(mode, host string, noTLS bool) error {
	if err := ValidateMQTTExposure(mode); err != nil {
		return err
	}
	if mode == "" {
		if host != "" {
			return ValidateMQTTHost(host)
		}
		return nil
	}
	_, err := settleMQTTExposure("", "", mode, host, noTLS)
	return err
}
