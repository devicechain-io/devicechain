// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/eventenvelope"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/fatih/color"
	nats "github.com/nats-io/nats.go"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

// FirstEnvelopeReaderRelease is the first release whose services all READ event envelopes (and
// whose DETECT carries the per-event duplicate guard). A release below it dead-letters an
// envelope it meets, so an instance must not move below it while the hot streams may hold one.
// The one place the cutoff is named.
const FirstEnvelopeReaderRelease = "1.1.0"

const (
	// AnnotationEnvelopesEnabledAt is set on the Instance when a run of dcctl turns resolved event
	// envelopes ON (an RFC 3339 time), and is never removed: an instance that has ever written
	// envelopes may still hold some.
	AnnotationEnvelopesEnabledAt = "core.devicechain.io/envelopes-enabled-at"
	// AnnotationEnvelopesOffMarks is set when envelopes are OFF again: a JSON map of each hot
	// stream to the sequence of its newest message at that moment. Every envelope is at or below
	// its stream's mark (the producer had stopped), so once a stream's oldest retained message is
	// above the mark none remains. Removed when envelopes are turned on again.
	AnnotationEnvelopesOffMarks = "core.devicechain.io/envelopes-off-marks"
)

// envelopeStreamProbe reads the hot streams from the broker; a seam, because the real one needs one.
var envelopeStreamProbe = probeStreamsForEnvelopes

// hotStreams are the streams envelopes are written to.
var hotStreams = streams.EnvelopeCarriers()

// envelopeSampleTail is how many of the newest messages of each hot stream the probe inspects,
// besides the oldest retained one.
const envelopeSampleTail = 256

// envelopeReadTimeout bounds one message read. A var so a test can shorten it.
var envelopeReadTimeout = 10 * time.Second

type streamBounds struct{ First, Last uint64 }

// envelopeProbeResult is what the broker says about the hot streams.
type envelopeProbeResult struct {
	Bounds map[string]streamBounds
	// Sample describes the first sampled message that is an envelope, "" when none is.
	Sample string
}

// envelopeFacts is everything the downgrade decision rests on.
type envelopeFacts struct {
	From, To string
	// FlagOn is the deployed configuration's envelope switch, and Switches names the ones that are
	// on (envelopeSwitchNames), for the refusal text.
	FlagOn   bool
	Switches []string
	// EnabledAt and OffMarks are the Instance's annotations, "" when absent.
	EnabledAt string
	OffMarks  string
	// Allow is --allow-envelope-downgrade.
	Allow bool
}

var coreSemver = regexp.MustCompile(`^v?([0-9]+)\.([0-9]+)\.([0-9]+)(?:[-+].*)?$`)

// releaseCore parses the major.minor.patch of a release tag. A pre-release keeps its core: the
// release candidates of a version carry what the version carries. ok is false for a tag that is
// not a release (a locally built image, "dev").
func releaseCore(v string) (core [3]int, ok bool) {
	m := coreSemver.FindStringSubmatch(strings.TrimSpace(v))
	if m == nil {
		return core, false
	}
	for i := 0; i < 3; i++ {
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return core, false
		}
		core[i] = n
	}
	return core, true
}

func belowEnvelopeReaders(v string) (below, known bool) {
	c, ok := releaseCore(v)
	if !ok {
		return false, false
	}
	cut, _ := releaseCore(FirstEnvelopeReaderRelease)
	for i := 0; i < 3; i++ {
		if c[i] != cut[i] {
			return c[i] < cut[i], true
		}
	}
	return false, true
}

// checkEnvelopeDowngrade refuses a move to a release that cannot read envelopes while the hot
// streams may hold one.
//
// THE RULE IS EXACT, not a guess about the streams. A move is allowed only if the switch is off
// in the deployed configuration AND either the instance has never had it on (no enabled-at
// annotation) or every hot stream's oldest retained message is above the mark recorded when it
// was turned off (so every envelope has expired or been removed). On top of that, a sample of the
// streams (the oldest retained message and the newest envelopeSampleTail of each) is ADDITIONAL
// evidence that refuses: it catches a switch set outside dcctl.
//
// Never refused: no image move (to == from), a target at or above FirstEnvelopeReaderRelease, a
// move from a release below it (no producer of envelopes ever ran there), and --allow-envelope-
// downgrade, which says so loudly.
//
// FAIL CLOSED. A target that is not a release tag ("dev", a local build) is treated as below the
// cutoff, and a probe that cannot answer (a stream missing, an unreadable annotation, no broker)
// refuses, because "I could not tell" must not read as "no envelopes".
func checkEnvelopeDowngrade(ctx context.Context, f envelopeFacts, probe func(context.Context) (envelopeProbeResult, error)) error {
	if f.To == f.From {
		return nil
	}
	if below, known := belowEnvelopeReaders(f.To); known && !below {
		return nil
	}
	if below, known := belowEnvelopeReaders(f.From); known && below {
		return nil
	}
	target := f.To
	if _, known := releaseCore(f.To); !known {
		target += " (not a release tag, so it cannot be shown to read envelopes)"
	}
	if f.Allow {
		fmt.Println(color.RedString("  WARNING: --allow-envelope-downgrade skips the check that no event envelope remains on the "+
			"streams. A release below %s dead-letters an envelope it meets, and the events in it are lost to that service.",
			FirstEnvelopeReaderRelease))
		return nil
	}
	fix := fmt.Sprintf("Set every switch under infrastructure.messaging.envelopes (resolved, inbound) to false (`dcctl upgrade <provider> <instance> --values <file>` "+
		"sets it under instance.config.infrastructure.messaging.envelopes), then let inbound-events and resolved-events drain or "+
		"wait out their retention, and upgrade again. Or move to %s or later. Nothing has been changed.", FirstEnvelopeReaderRelease)
	refuse := func(why string) error {
		return fmt.Errorf("moving to %s is refused: %s, and a release below %s dead-letters an event envelope. %s",
			target, why, FirstEnvelopeReaderRelease, fix)
	}
	if f.FlagOn {
		names := f.Switches
		if len(names) == 0 {
			names = []string{"resolved"}
		}
		return refuse(fmt.Sprintf("event envelopes are switched ON (%s)", strings.Join(names, ", ")))
	}
	res, err := probe(ctx)
	if err != nil {
		return refuse(fmt.Sprintf("the hot streams could not be checked (%v)", err))
	}
	if res.Sample != "" {
		return refuse(res.Sample + " holds an event envelope")
	}
	if f.EnabledAt == "" {
		return nil // never turned on through dcctl, and the sample found none
	}
	if f.OffMarks == "" {
		return refuse(fmt.Sprintf("envelopes were turned on at %s and no record was made of when they were turned off. "+
			"Run `dcctl upgrade` once more at the version the instance runs, which records it", f.EnabledAt))
	}
	var marks map[string]uint64
	if err := json.Unmarshal([]byte(f.OffMarks), &marks); err != nil {
		return refuse(fmt.Sprintf("the record of when envelopes were turned off cannot be read (%v)", err))
	}
	var remaining []string
	for _, suffix := range hotStreams {
		mark, ok := marks[suffix]
		if !ok {
			return refuse(fmt.Sprintf("the record of when envelopes were turned off has no mark for %s", suffix))
		}
		b, ok := res.Bounds[suffix]
		if !ok {
			return refuse(fmt.Sprintf("%s was not read", suffix))
		}
		if b.First <= mark {
			remaining = append(remaining, fmt.Sprintf("%s still holds messages from before envelopes were turned off (oldest %d, mark %d)", suffix, b.First, mark))
		}
	}
	if len(remaining) > 0 {
		sort.Strings(remaining)
		return refuse(strings.Join(remaining, "; "))
	}
	return nil
}

// precheckEnvelopeDowngrade is checkEnvelopeDowngrade for an upgrade run, asked before anything
// is written.
func precheckEnvelopeDowngrade(ctx context.Context, st *State, allow bool) error {
	err := checkEnvelopeDowngrade(ctx, envelopeFacts{
		From: st.FromImageVersion, To: st.ImageVersion, FlagOn: st.EnvelopesResolvedOn, Switches: st.EnvelopeSwitches,
		EnabledAt: st.EnvelopesEnabledAt, OffMarks: st.EnvelopesOffMarks, Allow: allow,
	}, func(ctx context.Context) (envelopeProbeResult, error) {
		return envelopeStreamProbe(ctx, st.Instance, st.KubeContext)
	})
	if err != nil {
		return fmt.Errorf("upgrading instance %q: %w", st.Instance, err)
	}
	return nil
}

// precheckBootstrapEnvelopeDowngrade is the same check for a bootstrap re-run over an existing
// declaration whose image version it would change (a restore retry or a carve-out re-run can):
// the version moves there too, so the refusal has to.
func precheckBootstrapEnvelopeDowngrade(ctx context.Context, st *State, existing *dcv1beta1.Instance) error {
	if existing == nil || existing.Spec.ImageVersion == st.ImageVersion {
		return nil
	}
	_, _, typed, err := kubeClients(st.KubeContext)
	if err != nil {
		return fmt.Errorf("building kube clients to check for event envelopes: %w", err)
	}
	flagOn := false
	var switches []string
	cfg, err := deployedInstanceConfig(ctx, typed, st.Instance)
	switch {
	case err == nil:
		flagOn = envelopesOn(cfg.Infrastructure.Messaging.Envelopes)
		switches = envelopeSwitchNames(cfg.Infrastructure.Messaging.Envelopes)
	case apierrors.IsNotFound(err):
		return nil // no configuration document: no service has run, so no envelope exists
	default:
		return fmt.Errorf("reading the configuration instance %q is running on, to check for event envelopes: %w", st.Instance, err)
	}
	err = checkEnvelopeDowngrade(ctx, envelopeFacts{
		From: existing.Spec.ImageVersion, To: st.ImageVersion, FlagOn: flagOn, Switches: switches,
		EnabledAt: existing.Annotations[AnnotationEnvelopesEnabledAt], OffMarks: existing.Annotations[AnnotationEnvelopesOffMarks],
	}, func(ctx context.Context) (envelopeProbeResult, error) {
		return envelopeStreamProbe(ctx, st.Instance, st.KubeContext)
	})
	if err != nil {
		return fmt.Errorf("bootstrapping instance %q over its existing declaration: %w", st.Instance, err)
	}
	return nil
}

// overlayEnvelopesOn reads the envelope switches out of a values overlay: either one (resolved
// or inbound) puts envelopes on the hot streams.
func overlayEnvelopesOn(overlay map[string]interface{}) bool {
	for _, key := range []string{"resolved", "inbound"} {
		var cur interface{} = overlay
		for _, k := range []string{"instance", "config", "infrastructure", "messaging", "envelopes", key} {
			m, ok := cur.(map[string]interface{})
			if !ok {
				cur = nil
				break
			}
			cur = m[k]
		}
		if on, _ := cur.(bool); on {
			return true
		}
	}
	return false
}

// envelopesOn is whether a deployed configuration has either envelope switch on.
func envelopesOn(e config.EnvelopesConfiguration) bool { return e.Resolved || e.Inbound }

// envelopeSwitchNames names the envelope switches that are on.
func envelopeSwitchNames(e config.EnvelopesConfiguration) []string {
	var out []string
	if e.Resolved {
		out = append(out, "resolved")
	}
	if e.Inbound {
		out = append(out, "inbound")
	}
	return out
}

// recordEnvelopesEnabled makes the instance remember that it has turned envelopes on: before the
// run that turns them on moves anything, so a run that dies half-way still leaves the record. A
// switch that is already on but was never recorded (it was set outside dcctl) is recorded too. It
// clears the off-marks: they described an earlier off.
func recordEnvelopesEnabled(ctx context.Context, dyn dynamic.Interface, instance string, turningOn bool) error {
	return mutateInstanceAnnotations(ctx, dyn, instance, func(a map[string]string) bool {
		if a[AnnotationEnvelopesEnabledAt] != "" && !turningOn {
			return false
		}
		if turningOn || a[AnnotationEnvelopesEnabledAt] == "" {
			a[AnnotationEnvelopesEnabledAt] = time.Now().UTC().Format(time.RFC3339)
			if !turningOn {
				a[AnnotationEnvelopesEnabledAt] += " (set outside dcctl)"
			}
		}
		delete(a, AnnotationEnvelopesOffMarks)
		return true
	})
}

// settleEnvelopeMarks runs at the end of a successful upgrade. When envelopes are off and the
// instance has had them on, it records each hot stream's newest sequence as the mark, unless one
// is already recorded. Taken after the rollout, when no pod writes envelopes any more, it is
// exact; taken by a later run after a failed one it is later than the truth, which is safe (more
// has to expire before a downgrade).
func settleEnvelopeMarks(ctx context.Context, dyn dynamic.Interface, st *State) error {
	inst, err := readInstanceCR(ctx, dyn, st.Instance)
	if err != nil || inst == nil {
		return err
	}
	if inst.Annotations[AnnotationEnvelopesEnabledAt] == "" || inst.Annotations[AnnotationEnvelopesOffMarks] != "" {
		return nil
	}
	res, err := envelopeStreamProbe(ctx, st.Instance, st.KubeContext)
	if err != nil {
		return fmt.Errorf("recording when envelopes were turned off: %w", err)
	}
	marks := map[string]uint64{}
	for _, suffix := range hotStreams {
		b, ok := res.Bounds[suffix]
		if !ok {
			return fmt.Errorf("recording when envelopes were turned off: %s was not read", suffix)
		}
		marks[suffix] = b.Last
	}
	raw, _ := json.Marshal(marks)
	return mutateInstanceAnnotations(ctx, dyn, st.Instance, func(a map[string]string) bool {
		if a[AnnotationEnvelopesOffMarks] != "" {
			return false
		}
		a[AnnotationEnvelopesOffMarks] = string(raw)
		return true
	})
}

// mutateInstanceAnnotations reads the Instance, lets change edit its annotations, and writes it
// back as a compare-and-swap when change reports an edit.
func mutateInstanceAnnotations(ctx context.Context, dyn dynamic.Interface, instance string, change func(map[string]string) bool) error {
	inst, err := readInstanceCR(ctx, dyn, instance)
	if err != nil {
		return err
	}
	if inst == nil {
		return fmt.Errorf("the declaration for instance %q is not there to record the envelope switch on", instance)
	}
	if inst.Annotations == nil {
		inst.Annotations = map[string]string{}
	}
	if !change(inst.Annotations) {
		return nil
	}
	obj, err := instanceToUnstructured(inst)
	if err != nil {
		return err
	}
	if _, err := dyn.Resource(instanceGVR).Update(ctx, obj, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("recording the envelope switch on instance %q: %w", instance, err)
	}
	return nil
}

// probeStreamsForEnvelopes reads the hot streams through a port-forward to a broker pod, as the
// operator: each stream's first and last sequence, and a sample of message headers.
func probeStreamsForEnvelopes(ctx context.Context, instanceId, kubeContext string) (envelopeProbeResult, error) {
	restCfg, err := RestConfig(kubeContext)
	if err != nil {
		return envelopeProbeResult{}, err
	}
	typed, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return envelopeProbeResult{}, err
	}
	cfg, err := deployedInstanceConfigWithLogins(ctx, typed, instanceId)
	if err != nil {
		return envelopeProbeResult{}, err
	}
	ns := InstanceNamespace(instanceId)
	pods, err := natsPods(ctx, typed, ns)
	if err != nil {
		return envelopeProbeResult{}, err
	}
	if len(pods) == 0 {
		return envelopeProbeResult{}, fmt.Errorf("no broker pod matched %q in namespace %s", natsPodSelector, ns)
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	local, stop, err := forwardPortCtx(ctx, restCfg, ns, pods[0].Name, int(cfg.Infrastructure.Nats.Port))
	if err != nil {
		return envelopeProbeResult{}, err
	}
	defer stop()
	nc, err := dialBrokerCtx(ctx, fmt.Sprintf("nats://127.0.0.1:%d", local), cfg.Infrastructure.Nats)
	if err != nil {
		return envelopeProbeResult{}, err
	}
	defer nc.Close()
	js, err := nc.JetStream(nats.Context(ctx))
	if err != nil {
		return envelopeProbeResult{}, err
	}
	return sampleStreams(ctx, js, instanceId)
}

// streamReader is the part of JetStream the probe uses.
type streamReader interface {
	StreamInfo(stream string, opts ...nats.JSOpt) (*nats.StreamInfo, error)
	GetMsg(name string, seq uint64, opts ...nats.JSOpt) (*nats.RawStreamMsg, error)
}

// sampleStreams reads each hot stream's bounds and a sample of its messages. A stream that does
// not exist is an ERROR: on a running instance both hot streams exist, so a missing one means the
// instance is not what it was taken for, and "nothing there" must not read as "no envelopes".
// The reads are served by the stream's leader.
func sampleStreams(ctx context.Context, js streamReader, instanceId string) (envelopeProbeResult, error) {
	out := envelopeProbeResult{Bounds: map[string]streamBounds{}}
	for _, suffix := range hotStreams {
		name := messaging.StreamName(instanceId, suffix)
		info, err := js.StreamInfo(name)
		if err != nil {
			if errors.Is(err, nats.ErrStreamNotFound) {
				return out, fmt.Errorf("stream %s does not exist on a running instance", name)
			}
			return out, fmt.Errorf("reading stream %s: %w", name, err)
		}
		st := info.State
		out.Bounds[suffix] = streamBounds{First: st.FirstSeq, Last: st.LastSeq}
		if st.Msgs == 0 || out.Sample != "" {
			continue
		}
		seqs := []uint64{st.FirstSeq}
		for s := st.LastSeq; s > st.FirstSeq && uint64(len(seqs)) <= envelopeSampleTail; s-- {
			seqs = append(seqs, s)
		}
		for _, seq := range seqs {
			rctx, cancel := context.WithTimeout(ctx, envelopeReadTimeout)
			m, err := js.GetMsg(name, seq, nats.Context(rctx))
			cancel()
			if err != nil {
				if errors.Is(err, nats.ErrMsgNotFound) {
					continue // deleted or expired between the info and the read
				}
				if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, nats.ErrTimeout) || errors.Is(err, nats.ErrNoResponders) ||
					strings.Contains(strings.ToLower(err.Error()), "permissions violation") {
					// The broker drops, without answering, a request its login may not make, so a
					// denied read looks exactly like a read that timed out.
					return out, fmt.Errorf("reading %s message %d was denied or went unanswered (%v): the broker administrator login "+
						"most likely lacks $JS.API.STREAM.MSG.GET.%s and $JS.API.DIRECT.GET.%s; the broker log shows a Permissions Violation "+
						"if so", name, seq, err, name, name)
				}
				return out, fmt.Errorf("reading %s message %d: %w", name, seq, err)
			}
			if m.Header.Get(eventenvelope.HeaderName) != "" {
				out.Sample = fmt.Sprintf("%s (message %d)", suffix, seq)
				break
			}
		}
	}
	return out, nil
}
