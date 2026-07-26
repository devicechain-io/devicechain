// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/replication"
	nats "github.com/nats-io/nats.go"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
)

// HaVerifyOptions selects the instance to check and how to reach its broker.
type HaVerifyOptions struct {
	// KubeContext is the kubeconfig context; empty uses the current one.
	KubeContext string
	// InstanceId names the instance. Its namespace, its config Secret and its
	// object names all derive from it.
	InstanceId string
	// Replicas overrides the declared replica factor. Zero — the normal case —
	// reads it from the instance configuration the services are actually running
	// on, which is the honest source: the question is whether the broker matches
	// what this instance CLAIMS, and a factor supplied on the command line is the
	// operator's belief rather than the instance's claim.
	Replicas int
	// NatsUrl bypasses the port-forward and dials this URL directly. For a rig
	// that already has a tunnel open, or a broker reachable without one.
	NatsUrl string
	// ProbeMqtt opens one MQTT connection before collecting, so the broker's own
	// $MQTT_* streams exist and their replica factor can be observed.
	//
	// Off by default because it MUTATES: on a broker no device has ever reached, it
	// causes four streams to be created. A check that changes what it inspects is
	// not one you can run against production at any time, and that property is
	// worth more than the convenience. On a validation rig it is exactly right —
	// see mqttProbe.
	ProbeMqtt bool
	// Settle is how long to keep re-checking while assertions are failing.
	//
	// Not leniency, and the distinction is the whole reason it is bounded. Raising a
	// replica factor is a RAFT peer-set reconfiguration followed by the new peers
	// catching up: for a real interval the object reports its new count while its
	// peers are NOT current, and a consumer group is created on its stream's leader
	// and remapped afterwards. Both are legitimately transient, and a check run one
	// second after a rollout that treated them as failures would be wrong far more
	// often than it was right.
	//
	// What it never does is turn a failure into a pass. The loop returns the moment
	// everything holds, and on expiry it returns the LAST report — every finding
	// intact — rather than a summary of how close it got.
	Settle time.Duration
	// Timeout bounds the whole check.
	Timeout time.Duration
}

// natsPodSelector matches the broker pods the OpenTofu nats module installs. It
// is the chart's own selector, restated here for the same reason the module
// restates it on the MQTT NodePort Service: the labels are the contract.
const natsPodSelector = "app.kubernetes.io/name=nats," +
	"app.kubernetes.io/instance=dc-nats,app.kubernetes.io/component=nats"

// VerifyReplication asserts an instance's live broker state against the
// replication that instance declares (ADR-020 A0, check A).
//
// It answers ONE question and answers it from observation: are this instance's
// streams, KV buckets, consumer groups and broker pods actually replicated the
// way its configuration says they are. Every input it judges comes from the
// cluster —
//
//	the declared factor  <- the instance-config Secret the pods mount
//	the object state     <- the broker, over a JetStream connection
//	the pod placement    <- the Kubernetes API
//
// — and none of it from what dcctl would have rendered. That distinction is the
// whole point: A0 shipped because every existing check compared our intent
// against our intent.
func VerifyReplication(ctx context.Context, opts HaVerifyOptions) (replication.Report, error) {
	var rep replication.Report
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}
	if opts.InstanceId == "" {
		return rep, fmt.Errorf("an instance id is required")
	}

	restCfg, err := RestConfig(opts.KubeContext)
	if err != nil {
		return rep, fmt.Errorf("building kube config: %w", err)
	}
	typed, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return rep, err
	}

	cfg, err := deployedInstanceConfig(ctx, typed, opts.InstanceId)
	if err != nil {
		return rep, err
	}
	declared := opts.Replicas
	if declared == 0 {
		declared = int(cfg.Infrastructure.Nats.StreamReplicas)
	}
	if declared < 1 {
		return rep, fmt.Errorf("the instance declares a stream replica factor of %d, which is "+
			"not a topology; nothing can be checked against it", declared)
	}

	pods, err := natsPods(ctx, typed)
	if err != nil {
		return rep, err
	}

	url := opts.NatsUrl
	if url == "" {
		if len(pods) == 0 {
			return rep, fmt.Errorf("no NATS pods matched %q in namespace %s, so there is "+
				"nothing to port-forward to. Pass --nats-url if the broker is reachable "+
				"another way", natsPodSelector, natsInfraNamespace)
		}
		local, stop, err := forwardPort(restCfg, natsInfraNamespace, pods[0].Name, int(cfg.Infrastructure.Nats.Port))
		if err != nil {
			return rep, fmt.Errorf("opening a port-forward to broker pod %s: %w", pods[0].Name, err)
		}
		defer stop()
		url = fmt.Sprintf("nats://127.0.0.1:%d", local)
	}

	if opts.ProbeMqtt {
		if len(pods) == 0 {
			return rep, fmt.Errorf("--probe-mqtt needs a broker pod to forward to and none was found")
		}
		if err := probeMqttVia(restCfg, pods[0].Name, cfg.Infrastructure.Nats); err != nil {
			return rep, err
		}
	}

	nc, err := dialBroker(url, cfg.Infrastructure.Nats)
	if err != nil {
		return rep, err
	}
	defer nc.Close()
	js, err := nc.JetStream()
	if err != nil {
		return rep, err
	}

	areas, err := deployedAreas(ctx, typed, opts.InstanceId)
	if err != nil {
		return rep, err
	}
	exp := messaging.ReplicationExpectation(opts.InstanceId, declared, areas)

	// One collect+verify attempt, over a connection and a pod listing that are
	// re-read each time: a settling cluster is one where the pods are also still
	// arriving.
	attempt := func() (replication.Report, error) {
		snap, err := replication.Collect(js, exp)
		if err != nil {
			return replication.Report{}, err
		}
		fresh, err := natsPods(ctx, typed)
		if err != nil {
			return replication.Report{}, err
		}
		snap.Pods = placedPods(fresh)
		return replication.Verify(snap, exp), nil
	}

	rep, err = attempt()
	if err != nil || rep.OK() || opts.Settle <= 0 {
		return rep, err
	}
	deadline := time.Now().Add(opts.Settle)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return rep, ctx.Err()
		case <-time.After(settleInterval):
		}
		next, err := attempt()
		if err != nil {
			// A collection error mid-settle is not a verdict either way. Keep the last
			// real report rather than replacing it with a transport problem.
			continue
		}
		rep = next
		if rep.OK() {
			return rep, nil
		}
	}
	return rep, nil
}

// settleInterval paces the re-check. Long enough not to hammer the broker's
// JetStream API with a full stream listing, short enough that a drill is not
// waiting on the poll.
const settleInterval = 5 * time.Second

// deployedInstanceConfig reads the instance configuration out of the Secret the
// pods mount at /etc/dci-config/instance.
//
// This is the same document every service loads at startup, which is what makes
// the declared factor read here the instance's actual claim rather than a
// re-render of what dcctl thinks it installed. An instance whose Secret was
// edited by hand, or installed by an older dcctl, or managed externally via
// instance.existingSecret, is judged on what it is running.
func deployedInstanceConfig(ctx context.Context, typed *kubernetes.Clientset, instanceId string) (*config.InstanceConfiguration, error) {
	name := fmt.Sprintf("dci-%s-config", instanceId)
	sec, err := typed.CoreV1().Secrets(instanceId).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("reading the instance configuration from Secret %s/%s: %w "+
			"(if this instance sets instance.existingSecret, pass --replicas to state the "+
			"factor it should hold)", instanceId, name, err)
	}
	raw, ok := sec.Data["instance"]
	if !ok {
		return nil, fmt.Errorf("Secret %s/%s has no \"instance\" key", instanceId, name)
	}
	cfg := &config.InstanceConfiguration{}
	if err := json.Unmarshal(raw, cfg); err != nil {
		return nil, fmt.Errorf("decoding the instance configuration: %w", err)
	}
	// The services call ApplyDefaults before reading any of this, so a key omitted
	// from the Secret must be judged the way they will judge it and not as a zero.
	cfg.ApplyDefaults()
	return cfg, nil
}

// deployedAreas reads which functional areas are actually running, from the
// Deployments in the instance namespace (the chart names each one for its area).
//
// Observed rather than resolved from a profile name, for the same reason
// everything else here is: a profile is what was asked for. What decides whether
// a stream can exist is which services are running, and those are two different
// facts on any instance that has been upgraded, partially rolled out, or edited.
//
// An empty result is NOT an error and NOT a relaxation. ReplicationExpectation
// reads "no known areas" as "require every stream", so a namespace this cannot
// read produces the strictest check rather than the most forgiving one — which is
// the right direction for a failure whose cause is unknown.
func deployedAreas(ctx context.Context, typed *kubernetes.Clientset, instanceId string) ([]string, error) {
	list, err := typed.AppsV1().Deployments(instanceId).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing the deployed functional areas in namespace %s: %w",
			instanceId, err)
	}
	out := make([]string, 0, len(list.Items))
	for _, d := range list.Items {
		out = append(out, d.Name)
	}
	return out, nil
}

// natsPods lists the broker pods, for the placement half of the check.
func natsPods(ctx context.Context, typed *kubernetes.Clientset) ([]corev1.Pod, error) {
	list, err := typed.CoreV1().Pods(natsInfraNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: natsPodSelector,
	})
	if err != nil {
		return nil, fmt.Errorf("listing broker pods: %w", err)
	}
	return list.Items, nil
}

// placedPods keeps only the broker pods that are actually RUNNING ON A NODE.
//
// Split from the listing above so the rule can be asserted without a cluster,
// because the rule is where this can be wrong and the wrong answer is a quiet
// pass. A Pending pod — the state a hard topologySpreadConstraint produces when
// there is nowhere left to put a server, which is precisely the failure the
// placement check exists to catch — carries an EMPTY node name. Counted, three
// pods on two nodes plus one unscheduled read as three distinct "nodes", one of
// them the empty string, and the distinct-node assertion passes on a cluster
// running two servers.
//
// So this is not a tidy-up. It is the difference between the placement check
// working and the placement check being decorative in exactly the case it was
// written for.
func placedPods(pods []corev1.Pod) []replication.Pod {
	var out []replication.Pod
	for _, p := range pods {
		if p.Status.Phase != corev1.PodRunning || p.Spec.NodeName == "" {
			continue
		}
		out = append(out, replication.Pod{Name: p.Name, Node: p.Spec.NodeName})
	}
	return out
}

// mqttListenerPort is the broker's MQTT listener, set by the OpenTofu nats
// module. It is not in the instance configuration because no DeviceChain service
// is an MQTT client — under ADR-030 the gateway is nats-server's own listener and
// the services consume from JetStream behind it.
const mqttListenerPort = 1883

// probeMqttVia opens its own port-forward to the MQTT listener.
//
// A second tunnel rather than reusing the JetStream one: they are different ports
// on the same pod, and the probe's tunnel is closed the moment the connection is
// done, so the broker's MQTT accounting is not left holding a session while the
// rest of the check runs.
func probeMqttVia(restCfg *rest.Config, pod string, natscfg config.NatsConfiguration) error {
	local, stop, err := forwardPort(restCfg, natsInfraNamespace, pod, mqttListenerPort)
	if err != nil {
		return fmt.Errorf("opening a port-forward to the MQTT listener on %s: %w", pod, err)
	}
	defer stop()

	tlsCfg, err := natscfg.TLSConfig(natscfg.Hostname)
	if err != nil {
		return err
	}
	if tlsCfg != nil {
		tlsCfg.ServerName = natscfg.Hostname
	}
	addr := fmt.Sprintf("127.0.0.1:%d", local)
	if err := mqttProbe(addr, tlsCfg, natscfg.Auth.User, natscfg.Auth.Password); err != nil {
		return fmt.Errorf("MQTT probe: %w", err)
	}
	return nil
}

// dialBroker connects with the instance's own TLS material and credentials.
//
// ServerName is pinned to the configured hostname rather than to the loopback
// address the port-forward listens on. The broker's certificate is issued for its
// in-cluster names (see modules/nats), so verifying against "127.0.0.1" would
// fail on every TLS-enabled instance — and the fix that suggests itself,
// InsecureSkipVerify, would quietly make this the one tool that does not check
// the thing it is inspecting.
func dialBroker(url string, natscfg config.NatsConfiguration) (*nats.Conn, error) {
	opts := []nats.Option{
		nats.Name("dcctl-ha-verify"),
		// No reconnect. This is a one-shot check, and a client that retries in the
		// background turns "the broker is unreachable" into a hang.
		nats.MaxReconnects(0),
		nats.Timeout(10 * time.Second),
	}
	tlsConfig, err := natscfg.TLSConfig(natscfg.Hostname)
	if err != nil {
		return nil, err
	}
	if tlsConfig != nil {
		tlsConfig.ServerName = natscfg.Hostname
		opts = append(opts, nats.Secure(tlsConfig))
	}
	if natscfg.Auth.User != "" {
		opts = append(opts, nats.UserInfo(natscfg.Auth.User, natscfg.Auth.Password))
	}
	nc, err := nats.Connect(url, opts...)
	if err != nil {
		return nil, fmt.Errorf("connecting to the broker at %s: %w", url, err)
	}
	return nc, nil
}

// forwardPort opens a port-forward to one pod and returns the local port.
//
// dcctl does this itself rather than telling the operator to run `kubectl
// port-forward` alongside it. A tunnel held open by a separate process is a
// second thing that can die mid-check, and when it does the symptom is a
// connection error in the middle of a replication report — which reads like a
// broken broker. Owning the tunnel means its lifetime is the check's lifetime.
//
// Port 0 asks the kernel for a free local port, so two checks can run at once and
// neither collides with anything already bound.
func forwardPort(restCfg *rest.Config, namespace, pod string, remote int) (int, func(), error) {
	roundTripper, upgrader, err := spdy.RoundTripperFor(restCfg)
	if err != nil {
		return 0, nil, err
	}
	host, err := url.Parse(restCfg.Host)
	if err != nil {
		return 0, nil, fmt.Errorf("parsing the API server address %q: %w", restCfg.Host, err)
	}
	target := &url.URL{
		Scheme: "https",
		Host:   host.Host,
		Path: fmt.Sprintf("%s/api/v1/namespaces/%s/pods/%s/portforward",
			strings.TrimSuffix(host.Path, "/"), namespace, pod),
	}
	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: roundTripper}, http.MethodPost, target)

	stopChan := make(chan struct{})
	readyChan := make(chan struct{})
	fw, err := portforward.New(dialer, []string{fmt.Sprintf("0:%d", remote)},
		stopChan, readyChan, io.Discard, io.Discard)
	if err != nil {
		return 0, nil, err
	}
	errChan := make(chan error, 1)
	go func() { errChan <- fw.ForwardPorts() }()

	select {
	case <-readyChan:
	case err := <-errChan:
		return 0, nil, fmt.Errorf("port-forward to %s/%s failed: %w", namespace, pod, err)
	case <-time.After(20 * time.Second):
		close(stopChan)
		return 0, nil, fmt.Errorf("port-forward to %s/%s did not become ready", namespace, pod)
	}
	ports, err := fw.GetPorts()
	if err != nil || len(ports) == 0 {
		close(stopChan)
		return 0, nil, fmt.Errorf("port-forward to %s/%s reported no local port: %w", namespace, pod, err)
	}
	return int(ports[0].Local), func() { close(stopChan) }, nil
}
