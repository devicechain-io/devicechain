// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/fatih/color"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

// localMQTTNodePort is the node port the local kind configuration maps host 1883 to
// (deploy/local/kind-cluster.yaml). It is the first of localMQTTNodePorts, and the one every
// local instance had before there were more.
const localMQTTNodePort = 31883

// localMQTTNodePorts are the node ports a local instance's MQTT gateway may be given, in the order
// a new instance tries them. The embedded kind configuration maps each to a host port (31883 to
// 1883, 31884 to 1884, 31885 to 1885), so a second and a third instance on one local cluster
// reach their brokers from the host too. A cluster created before the extra maps publishes only
// the first; which ones a cluster publishes is read from it (publishedMQTTNodePorts), never assumed.
var localMQTTNodePorts = []int32{localMQTTNodePort, 31884, 31885}

// clusterSingletons are the things on a cluster only one instance can hold: the local MQTT
// node port, and an ingress host. Everything else an instance runs is in its own
// namespace; these are cluster-wide by Kubernetes' own rules.
type clusterSingletons struct {
	// MQTTNodePortHolder is the namespace of a Service already holding localMQTTNodePort,
	// other than this instance's own. Empty when the port is free for this instance.
	MQTTNodePortHolder string
	// MQTTNodePortHolders maps each of localMQTTNodePorts another instance's Service holds to that
	// instance's namespace. Nil when none is held.
	MQTTNodePortHolders map[int32]string
	// OwnMQTTNodePort is the one of localMQTTNodePorts this instance's own Service already holds,
	// or 0. An instance keeps the port it has.
	OwnMQTTNodePort int32
	// HostHolder is the namespace of an Ingress, Gateway or HTTPRoute already serving this instance's host,
	// other than this instance's own. Empty when no other instance serves it.
	HostHolder string
}

// readClusterSingletons is the render step's read of what other instances already hold.
// Indirected, like the step's other cluster reads, so the decision can be exercised
// without one.
var readClusterSingletons = func(ctx context.Context, kubeContext, instance, host string) (clusterSingletons, error) {
	dyn, _, typed, err := kubeClients(kubeContext)
	if err != nil {
		return clusterSingletons{}, fmt.Errorf("connecting to the cluster to see what other instances hold: %w", err)
	}
	svcs, err := typed.CoreV1().Services(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return clusterSingletons{}, fmt.Errorf("listing Services to see whether the MQTT node port is taken: %w", err)
	}
	ings, err := typed.NetworkingV1().Ingresses(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return clusterSingletons{}, fmt.Errorf("listing Ingresses to see whether host %q is taken: %w", host, err)
	}
	// 🔴 GATEWAYS AND HTTPROUTES CLAIM A HOST TOO, and the controller that serves them picks
	// one claimant just as quietly: a second Gateway listener naming another instance's host
	// was accepted and programmed, and its more specific routes took that instance's paths.
	// A cluster without the Gateway API CRDs has no such objects, and that is not an error.
	claims, err := listGatewayHostClaims(ctx, dyn)
	if err != nil {
		return clusterSingletons{}, fmt.Errorf("listing Gateways and HTTPRoutes to see whether host %q is taken: %w", host, err)
	}
	return singletonsFrom(svcs.Items, ings.Items, claims, instance, host), nil
}

// gatewayHostClaim is a host a Gateway listener or an HTTPRoute names, and where it lives.
type gatewayHostClaim struct {
	Namespace string
	Host      string
}

// listGatewayHostClaims reads the hosts every Gateway listener and HTTPRoute names. An
// absent CRD yields none.
func listGatewayHostClaims(ctx context.Context, dyn dynamic.Interface) ([]gatewayHostClaim, error) {
	var out []gatewayHostClaim
	for _, q := range []struct {
		gvr   schema.GroupVersionResource
		hosts func(obj map[string]interface{}) []string
	}{
		{gatewayGVR, func(obj map[string]interface{}) []string {
			var hs []string
			listeners, _, _ := unstructured.NestedSlice(obj, "spec", "listeners")
			for _, l := range listeners {
				if lm, ok := l.(map[string]interface{}); ok {
					if h, ok := lm["hostname"].(string); ok {
						hs = append(hs, h)
					}
				}
			}
			return hs
		}},
		{httpRouteGVR, func(obj map[string]interface{}) []string {
			hs, _, _ := unstructured.NestedStringSlice(obj, "spec", "hostnames")
			return hs
		}},
	} {
		list, err := dyn.Resource(q.gvr).Namespace(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
				continue
			}
			return nil, err
		}
		for _, item := range list.Items {
			for _, h := range q.hosts(item.Object) {
				out = append(out, gatewayHostClaim{Namespace: item.GetNamespace(), Host: h})
			}
		}
	}
	return out, nil
}

// singletonsFrom is readClusterSingletons' decision, with no cluster in it.
func singletonsFrom(svcs []corev1.Service, ings []networkingv1.Ingress, claims []gatewayHostClaim,
	instance, host string) clusterSingletons {
	var out clusterSingletons
	own := InstanceNamespace(instance)
	for _, s := range svcs {
		for _, p := range s.Spec.Ports {
			if !isLocalMQTTNodePort(p.NodePort) {
				continue
			}
			if s.Namespace == own {
				out.OwnMQTTNodePort = p.NodePort
				continue
			}
			if out.MQTTNodePortHolders == nil {
				out.MQTTNodePortHolders = map[int32]string{}
			}
			out.MQTTNodePortHolders[p.NodePort] = s.Namespace
			if p.NodePort == localMQTTNodePort {
				out.MQTTNodePortHolder = s.Namespace
			}
		}
	}
	for _, ing := range ings {
		if ing.Namespace == own {
			continue
		}
		for _, r := range ing.Spec.Rules {
			if r.Host == host {
				out.HostHolder = ing.Namespace
			}
		}
	}
	for _, c := range claims {
		if c.Namespace != own && strings.EqualFold(c.Host, host) {
			out.HostHolder = c.Namespace
		}
	}
	return out
}

func isLocalMQTTNodePort(p int32) bool {
	for _, c := range localMQTTNodePorts {
		if c == p {
			return true
		}
	}
	return false
}

// mqttNodePortTaken reports whether another instance holds node port p.
func (h clusterSingletons) mqttNodePortTaken(p int32) bool {
	if p == localMQTTNodePort && h.MQTTNodePortHolder != "" {
		return true
	}
	_, taken := h.MQTTNodePortHolders[p]
	return taken
}

// chooseNewMQTTNodePort is the node port a NEW local instance's MQTT gateway gets: the one its
// own Service already holds (a re-run keeps its port), else the first of the ports the cluster
// publishes that no other instance holds, else none (0), and the instance is reachable in-cluster
// only.
func chooseNewMQTTNodePort(held clusterSingletons, published []int32) int32 {
	if held.OwnMQTTNodePort != 0 {
		return held.OwnMQTTNodePort
	}
	for _, p := range published {
		if !held.mqttNodePortTaken(p) {
			return p
		}
	}
	return 0
}

// keptMQTTNodePort is the node port an UPGRADED instance's MQTT gateway keeps. An upgrade never
// hands an instance a port it did not have: it keeps the one its own Service holds, else the one
// the instance record says it was given (its Service may have been deleted by hand), else what
// every upgrade did before there were more ports: localMQTTNodePort when no other instance holds
// it, and none otherwise.
func keptMQTTNodePort(held clusterSingletons, recorded int32) int32 {
	if held.OwnMQTTNodePort != 0 {
		return held.OwnMQTTNodePort
	}
	if recorded != 0 && isLocalMQTTNodePort(recorded) && !held.mqttNodePortTaken(recorded) {
		return recorded
	}
	if !held.mqttNodePortTaken(localMQTTNodePort) {
		return localMQTTNodePort
	}
	return 0
}

// publishedMQTTNodePorts are the localMQTTNodePorts the kind cluster behind kubeContext publishes
// on the host, in order. A context that is not kind's, or a cluster whose published ports cannot
// be read, gets localMQTTNodePort alone: what every local instance got before there were more.
//
// ONE `docker port` read answers for all of them: a read per port could fail for the first and
// succeed for a later one, and a new instance would then skip a free localMQTTNodePort.
var publishedMQTTNodePorts = func(kubeContext string) []int32 {
	published, ok := KindPublishedHostPorts(kubeContext)
	if !ok {
		return []int32{localMQTTNodePort}
	}
	var out []int32
	for _, p := range localMQTTNodePorts {
		if _, ok := published[int(p)]; ok {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return []int32{localMQTTNodePort}
	}
	return out
}

// recordMQTTNodePort writes the node port an instance's MQTT gateway was given into its local
// instance record, beside the cluster and context it was built on. A missing record (an instance
// built elsewhere) is not an error: the instance's own Service is what an upgrade reads first.
var recordMQTTNodePort = func(instance string, port int32) error {
	rec, err := ReadInstanceRecord(instance)
	if errors.Is(err, ErrNoInstanceRecord) {
		return nil
	}
	if err != nil {
		return err
	}
	want := int(port)
	if port == 0 {
		// "None" is written down, so it is not read back as "never recorded": an instance with no
		// port must not be addressed at another instance's.
		want = MQTTNodePortNone
	}
	if rec.MQTTNodePort == want {
		return nil
	}
	rec.MQTTNodePort = want
	return WriteInstanceRecord(rec)
}

// recordedMQTTNodePort is the node port the local instance record says the instance was given,
// or 0.
//
// The record is a file a person can edit, so its value is compared against the set rather than
// narrowed: anything that is not one of localMQTTNodePorts reads as none.
var recordedMQTTNodePort = func(instance string) int32 {
	rec, err := ReadInstanceRecord(instance)
	if err != nil {
		return 0
	}
	for _, p := range localMQTTNodePorts {
		if int(p) == rec.MQTTNodePort {
			return p
		}
	}
	return 0
}

// recordMQTTNodePortAfterApply writes the node port the apply just exposed into the local instance
// record. Only after a successful apply, so the record never names a port the instance does not
// have, never in a rehearsal, and only where a port was decided at all. A failure to write it is a
// warning and never fails the run: the instance's own Service is what the next upgrade reads
// first, and the record is bookkeeping, as the bootstrap's own record write treats it.
func recordMQTTNodePortAfterApply(c console, st *State) {
	if st.DryRun || !monitoringSlim(st) {
		return
	}
	if _, decided := st.Values[mqttNodePortKey]; !decided {
		return
	}
	if err := recordMQTTNodePort(st.Instance, mqttNodePortOf(st)); err != nil {
		c.println(color.YellowString("  warning: could not record this instance's MQTT node port (%v); "+
			"`dcctl sim create` falls back to %d until it is", err, localMQTTNodePort))
	}
}

// mqttNodePortsHeldBy names who holds which local MQTT node ports, for the message that says
// none was free.
func mqttNodePortsHeldBy(held clusterSingletons, published []int32) string {
	var parts []string
	for _, p := range published {
		ns, ok := held.MQTTNodePortHolders[p]
		if !ok && p == localMQTTNodePort {
			ns, ok = held.MQTTNodePortHolder, held.MQTTNodePortHolder != ""
		}
		if ok {
			parts = append(parts, fmt.Sprintf("%d by namespace %q", p, ns))
		}
	}
	return strings.Join(parts, ", ")
}

// refuseAHostAnotherInstanceServes stops a bootstrap whose ingress host is already
// another instance's.
//
// 🔴 THE INGRESS CONTROLLER DOES NOT REFUSE THIS; IT PICKS ONE. Two Ingress objects
// claiming the same host on one class are both accepted, and the controller serves one
// of them — so the second instance's console and API routes quietly never answer, or
// quietly replace the first's. Neither instance reports anything wrong.
func refuseAHostAnotherInstanceServes(held clusterSingletons, instance, host string) error {
	if held.HostHolder == "" {
		return nil
	}
	return &ErrHostTaken{Instance: instance, Host: host, Holder: held.HostHolder}
}

// ErrConnectionBudget is the refusal of an instance the shared relational store has no
// connections left for, raised before anything is written. Typed for the same reason as
// ErrHostTaken.
type ErrConnectionBudget struct{ Err error }

func (e *ErrConnectionBudget) Error() string { return e.Err.Error() }
func (e *ErrConnectionBudget) Unwrap() error { return e.Err }

// readInstanceCredentialSecret reports whether this instance's own database-login Secret
// is still in its namespace.
//
// 🔴 IT IS THE ARTIFACT THAT TRAVELS WITH THE DATABASE. applyInfra writes this Secret one
// call before it creates the database (writeMintedSecrets, then provisionInstanceDatabase
// — an order TestApplyInfraAppliesOnlyTheInstanceInOrder pins), and it lives in the
// instance's namespace, which is where the root key lives too. So its absence beside a
// database that IS there says the namespace went and the key with it. See
// refuseAStoreAndKeyThatDoNotMatch.
//
// A missing namespace reports as a missing Secret, which is the same answer for the same
// reason. Every other failure is an error: "could not tell" is not "it is gone".
var readInstanceCredentialSecret = func(ctx context.Context, kubeContext, instance string) (bool, error) {
	_, _, typed, err := kubeClients(kubeContext)
	if err != nil {
		return false, fmt.Errorf("connecting to the cluster to look for instance %q's credentials: %w",
			instance, err)
	}
	name := instanceRdbSecretName(instance)
	ns := InstanceNamespace(instance)
	switch _, err := typed.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{}); {
	case err == nil:
		return true, nil
	case apierrors.IsNotFound(err):
		return false, nil
	default:
		return false, fmt.Errorf("reading Secret %s/%s to see whether this instance still holds the "+
			"credentials written beside its database: %w", ns, name, err)
	}
}

// precheckSharedStore asks the shared relational store the two things a bootstrap has to
// settle before it writes anything: whether this instance's connection limit would be
// admitted — without admitting it — and what the store already holds under this
// instance's name. Indirected so the step can be driven without a cluster.
//
// 🔴 ONE SESSION, TWO ANSWERS. Reaching the store means creating the provisioner over
// the primary's local socket and opening a port-forward to it; asking twice in one step
// would run that ALTER ROLE twice to learn two things one connection can answer. It is
// the same reason readClusterSingletons answers about the node port and the ingress host
// together rather than reading the cluster twice.
//
// A State with no install record has no store to ask, so it is not asked — and the zero
// instanceStore that comes back is storeStateUnknown, never storeStateAbsent. See the
// caller: it does not consult a state it did not read.
var precheckSharedStore = func(ctx context.Context, st *State) (instanceStore, error) {
	if st.Install == nil {
		return instanceStore{}, nil
	}
	limit, err := instanceConnectionLimit(st)
	if err != nil {
		return instanceStore{}, fmt.Errorf("sizing this instance's connection limit: %w", err)
	}
	rdb := st.Install.Outputs.Rdb
	var store instanceStore
	err = withProvisionerSession(ctx, st.KubeContext, rdb, func(q instanceDBQuerier) error {
		if err := admitInstance(ctx, q, st.Instance, connectionAdmission{Limit: limit, Budget: rdb.MaxConnections}); err != nil {
			return err
		}
		store, err = readInstanceStore(ctx, q, st.Instance)
		return err
	})
	if err != nil {
		if errors.Is(err, errNoConnectionBudget) {
			err = fmt.Errorf("%w%s", err, overlayBudgetHint(st))
		}
		return instanceStore{}, asBudgetRefusal(err)
	}
	return store, nil
}

// asBudgetRefusal types an admission refusal so the command layer can tell it from
// every other failure; anything else passes through unchanged.
func asBudgetRefusal(err error) error {
	if errors.Is(err, errNoConnectionBudget) {
		return &ErrConnectionBudget{Err: err}
	}
	return err
}

// ErrHostTaken is the refusal of a host another instance serves. Typed, because the
// command layer undoes the local record this run wrote on exactly this refusal: it fires
// before anything is written, so that record describes an instance that was never built.
// See PriorLocalState.
type ErrHostTaken struct {
	Instance string
	Host     string
	Holder   string
}

func (e *ErrHostTaken) Error() string {
	return fmt.Sprintf("host %q is already served by the instance in namespace %q, and an ingress "+
		"controller given two instances on one host serves only one of them — silently. Bootstrap %q "+
		"on a host of its own with --host (for a local cluster, e.g. --host %s.localhost)",
		e.Host, e.Holder, e.Instance, e.Instance)
}

// stepCheckClusterSingletons asks what this cluster already holds that this instance
// would have to take from it, and refuses what is not free: a host another instance
// serves, the namespace this instance is named after, and room on the shared store.
//
// 🔴 BEFORE ANYTHING IS WRITTEN. It sits right after the rebuild refusal and ahead of
// the instance declaration, which is this pipeline's first write: a refusal after it
// would leave a
// declaration for an instance that was never built, and the cluster would report holding
// it. The node port is not refused — the instance is built without it — but it is decided
// here, from the same read, and said. TestTheSingletonStepRunsBeforeAnythingIsWritten is
// what holds this step in that position, and the command layer's record rollback is keyed
// on the refusals raised here because of it.
func stepCheckClusterSingletons(ctx context.Context, st *State) error {
	if st.Values == nil {
		st.Values = map[string]string{}
	}
	host := ingressHostFor(st)
	if st.DryRun {
		// A rehearsal is often aimed at a cluster that does not exist yet, so the read is
		// best-effort — and what a real run would refuse is still said.
		held, err := readClusterSingletons(ctx, st.KubeContext, st.Instance, host)
		switch {
		case err != nil:
			out(ctx).wouldDo(fmt.Sprintf("check whether another instance already serves host %q — the read "+
				"failed (%v), and a real run would stop here", host, err))
		case refuseAHostAnotherInstanceServes(held, st.Instance, host) != nil:
			out(ctx).wouldDo(fmt.Sprintf("REFUSE: host %q is already served by the instance in namespace %q", host, held.HostHolder))
		}
		// The namespace half of the rehearsal, rehearsed separately because it is a second
		// cluster read rather than a second reading of the one above: against a cluster
		// that is not there yet NEITHER half can answer, and each says so about its own
		// question instead of one failure standing in for both.
		var refusal *ErrNamespaceUnavailable
		switch err := precheckInstanceNamespace(ctx, st); {
		case errors.As(err, &refusal):
			out(ctx).wouldDo("REFUSE: " + refusal.Error())
		case err != nil:
			out(ctx).wouldDo(fmt.Sprintf("check whether namespace %q is this instance's to build in — the "+
				"read failed (%v), and a real run would stop here", InstanceNamespace(st.Instance), err))
		}
		// Best-effort, like the reads above: a refusal and a failed read both stop a real
		// run, and the rehearsal says which it met without calling either a verdict.
		if p := databasePlacement(st); !p.IsZero() {
			if err := checkDatabasePlacement(ctx, st); err != nil {
				out(ctx).wouldDo(fmt.Sprintf("check that the nodes labelled %s can still take this instance's event "+
					"store -- the check did not pass (%v), and a real run would stop here", p.NodeSelector, err))
			}
		}
		if class := backupSnapshotClass(st); class != "" {
			if err := precheckSnapshotClass(ctx, st); err != nil {
				out(ctx).wouldDo(fmt.Sprintf("check VolumeSnapshotClass %s for this instance's event store -- the "+
					"check did not pass (%v), and a real run would stop here", class, err))
			}
		}
		// 🔴 THE STORE IS NOT REHEARSED, AND THAT IS SAID RATHER THAN LEFT OUT. Reaching it
		// means creating the provisioner inside the primary pod and port-forwarding to it —
		// writes, on a cluster a rehearsal may be aimed at before it exists. So the budget
		// has never been rehearsed, and what the store already holds cannot be either. A
		// rehearsal that simply omitted the one refusal whose cost is permanent would be
		// read as clearance for the run it cannot clear.
		if err := precheckValuesOverlay(ctx, st); err != nil {
			out(ctx).wouldDo(fmt.Sprintf("render the values overlay -- the check did not pass (%v), and "+
				"a real run would stop here", err))
		}
		if err := precheckObjectStore(ctx, st); err != nil {
			out(ctx).wouldDo(fmt.Sprintf("check the s3 credentials Secret -- the check did not pass (%v), and "+
				"a real run would stop here", err))
		}
		out(ctx).wouldDo(fmt.Sprintf("ask the relational store what it already holds for %q, and refuse to mint "+
			"a fresh root key over a database recovered from an archive — NOT rehearsed, because "+
			"reading the store means signing in to it", st.Instance))
		return nil
	}

	out(ctx).doing("checking what other instances on this cluster hold")
	held, err := readClusterSingletons(ctx, st.KubeContext, st.Instance, host)
	if err != nil {
		return out(ctx).fail("checking what other instances on this cluster hold", err)
	}
	if err := refuseAHostAnotherInstanceServes(held, st.Instance, host); err != nil {
		return err
	}
	// 🔴 AND THE NAMESPACE THIS INSTANCE IS ABOUT TO BE BUILT IN, WHICH IS THE ONE WHERE
	// BEING LATE COSTS A ROOT KEY. ensureNamespaceForRelease reaches the same verdict, but
	// it reaches it inside the infrastructure apply — on a real cluster a bootstrap named
	// after the monitoring namespace got that far having already written the secret-store
	// root key, a TLS private key and four database credentials into it.
	if err := precheckInstanceNamespace(ctx, st); err != nil {
		return err
	}
	// The VolumeSnapshotClass the cluster was installed with, checked again for THIS
	// instance's event store: the class may have been deleted or changed since the
	// install, and a bootstrap that found out at the apply would leave a declared,
	// half-built instance behind. Here nothing has been written.
	if err := precheckSnapshotClass(ctx, st); err != nil {
		return err
	}
	// The placement the cluster was installed with, checked again for THIS instance's
	// event store, for the same reason: the nodes it names may have been drained,
	// relabelled or retainted since. Here nothing has been written.
	if err := checkDatabasePlacement(ctx, st); err != nil {
		return err
	}
	// The object store's credentials Secret, which the operator named and dcctl will copy:
	// absent or incomplete, the pods would sit in CreateContainerConfigError for as long as
	// the readiness wait lasts. Here nothing has been written.
	if err := precheckObjectStore(ctx, st); err != nil {
		return err
	}
	// The operator's tuning, rendered with the chart while nothing has been written.
	if err := precheckValuesOverlay(ctx, st); err != nil {
		return err
	}
	// A bootstrap composes the instance from its flags, so one given no Sparkplug sources over an
	// instance that has some would silently remove them.
	if err := precheckSparkplugWithdrawal(ctx, st); err != nil {
		return err
	}
	// ...and the same for the LwM2M identities, whose keys it would delete along with them.
	if err := precheckLwm2mWithdrawal(ctx, st); err != nil {
		return err
	}
	// 🔴 AND THE SHARED RELATIONAL STORE, FOR THE SAME REASON, TWICE OVER.
	//
	// The connection budget is the refusal an operator is likeliest to meet, and meeting
	// it at the apply would leave a declared, namespaced, credentialed instance behind.
	// The apply admits again, under a lock — this is the early answer, not the enforcement.
	//
	// What the store already HOLDS is the one that cannot wait at all. A database sitting
	// there under this instance's name, owned by this instance's own login, is what a
	// relational restore leaves behind — and a bootstrap that mints a fresh root key over
	// it comes up green with every recovered secret sealed shut forever. That question was
	// already being asked, inside the apply, one line AFTER writeMintedSecrets put the
	// fresh key in the cluster. Here it can still change the outcome.
	//
	// The guard is the caller DECLINING TO ASK, not a second copy of the policy: with no
	// install record there is no store to reach, so there is no reading of it to reason
	// about, and inventing one would mean deciding from storeStateUnknown — which refuses.
	if st.Install != nil {
		store, err := precheckSharedStore(ctx, st)
		if err != nil {
			return err
		}
		if err := refuseAStoreAndKeyThatDoNotMatch(st, store, clusterEvidence{
			HoldsInstance: func() (bool, string, error) {
				held, err := readClusterInstances(ctx, st.KubeContext)
				if err != nil {
					return false, "", err
				}
				return held.holds(st.Instance), held.Source, nil
			},
			KeepsItsCredentials: func() (bool, error) {
				return readInstanceCredentialSecret(ctx, st.KubeContext, st.Instance)
			},
		}); err != nil {
			return err
		}
	}
	out(ctx).done()
	if !monitoringSlim(st) {
		return nil
	}
	published := publishedMQTTNodePorts(st.KubeContext)
	port := chooseNewMQTTNodePort(held, published)
	st.Values[mqttNodePortKey] = strconv.Itoa(int(port))
	switch {
	case port == 0:
		out(ctx).printf("  %s\n", color.WhiteString(fmt.Sprintf("every MQTT node port this cluster publishes on the "+
			"host is held by another instance (%s), so this instance's broker is reachable in-cluster only. "+
			"A local cluster created by this dcctl publishes %d of them", mqttNodePortsHeldBy(held, published),
			len(localMQTTNodePorts))))
	case port != localMQTTNodePort:
		out(ctx).printf("  %s\n", color.WhiteString(fmt.Sprintf("MQTT node port %d is held by another instance, so "+
			"this instance takes node port %d", localMQTTNodePort, port)))
	}
	return nil
}
