// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/user"
	"sync"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	apply "github.com/devicechain-io/dc-k8s/apply"
	dck8s "github.com/devicechain-io/dc-k8s/config"
)

// The claim is the lock one dcctl run holds while it mutates a cluster.
//
// WHY A LEASE AND NOT A FIELD ON THE INSTANCE. The operator writes
// status.conditions on that object. A controller-runtime Status().Update() is a
// PUT of the WHOLE subresource carrying the resourceVersion it read, so the
// collision is not a silent overwrite — a stale writer gets a Conflict. The real
// cost is quieter: each writer serialises its own typed struct, so a field the
// other declares and this one does not is dropped on the round trip. Two writers
// with two views of one subresource lose exactly what they disagree about, which
// here would be the liveness signal a reclaim decision is made from. Server-side
// apply with distinct field managers arbitrates that correctly, but only by
// binding every future status writer to SSA to solve a problem that does not have
// to exist. A separate object has one writer by construction.
//
// 🔴 WHY THE LOCK IS PER-CLUSTER AND NOT PER-INSTANCE, WHICH IS THE PART THAT IS
// EASY TO GET WRONG. The obvious design gives each instance its own Lease, and it
// locks the wrong thing. Almost everything a bootstrap touches is a cluster
// singleton: the Helm release is the constant helmReleaseName in the literal
// "default" namespace, the infrastructure root installs fixed-name releases into
// dc-system / cnpg-system / cert-manager, and the operator Deployment is applied
// cluster-wide. Two runs holding two different per-instance Leases are both
// "legal" and will still overwrite each other's release, because the resource
// they contend for has no instance in its name. The instance id is recorded on
// the Lease so the refusal can say WHICH instance holds it, but it is not the key.
//
// 🔑 The remaining hazard is not this lock's to fix and must not be hidden by it:
// one cluster genuinely holds one instance today, and this lock enforces "one run
// at a time", not "one instance per cluster". See the follow-up filed with slice 3.
//
// WHY IT HAS TO WORK NOW. The OpenTofu state backend is still local (see
// instanceStateDir), so the state lock that will eventually block a concurrent
// apply does not exist yet, and a design leaning on it would be a lock arriving
// several releases after the races it is meant to stop.
const (
	// claimLeaseName is the single Lease every dcctl run contends for. See above:
	// the contended resources are cluster-scoped, so the lock is too.
	claimLeaseName = "dcctl"

	// claimLeaseDuration is how long a Lease stays valid after its last renewal.
	claimLeaseDuration = 60 * time.Second

	// claimRenewInterval is how often the holder renews, and therefore also how
	// often it notices it has been reclaimed.
	//
	// The 6:1 ratio against claimLeaseDuration is the point of both numbers. A
	// ratio near 1:1 makes one slow API call indistinguishable from a dead
	// process, which is the misreading this whole mechanism exists to avoid.
	claimRenewInterval = 10 * time.Second

	// annotationClaimInstance records which instance the holder is working on.
	// Reporting only — the lock is not keyed by it.
	annotationClaimInstance = "core.devicechain.io/instance"
)

// ErrClaimLost is what a fenced run reports once the claim is no longer its own.
var ErrClaimLost = errors.New("this cluster was reclaimed by another operator")

// Claim is a held Lease plus the goroutine renewing it.
type Claim struct {
	instance string
	ns       string
	holder   string
	client   kubernetes.Interface

	stop     chan struct{}
	stopOnce sync.Once
	done     chan struct{}

	mu       sync.Mutex
	lost     error
	lastHeld time.Time
}

// Holder reports the identity this run wrote into the Lease.
func (c *Claim) Holder() string { return c.holder }

// newHolderIdentity builds the string a second operator reads when told the
// cluster is taken. It names a person, a machine and a process, because all three
// are things they can go and check — and it ends in a nonce, which is the part
// the protocol uses rather than the part a human reads.
//
// The nonce is what makes a reclaim detectable. Without it, a process that was
// reclaimed and then re-acquired by the same user on the same host with a
// recycled pid would compare equal to itself and carry on applying.
func newHolderIdentity() (string, error) {
	name := "unknown"
	if u, err := user.Current(); err == nil && u.Username != "" {
		name = u.Username
	}
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown"
	}
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Fail loudly. A holder identity that is not unique is not a holder
		// identity, and every safety property here is built on this string.
		return "", fmt.Errorf("generating a claim nonce: %w", err)
	}
	return fmt.Sprintf("%s@%s/%d/%s", name, host, os.Getpid(), hex.EncodeToString(b[:])), nil
}

// operatorNamespace reads the namespace the operator overlay declares.
//
// Read from the rendered manifests rather than hard-coded, for the same reason
// operatorDeployments does it: the namespace is a kustomize setting, and a copy
// of it here would be a second place to remember. A rename would otherwise leave
// dcctl taking its lock in a namespace nothing else uses — a lock that silently
// protects nothing.
//
// 🔴 This namespace is SHARED with the operator and is never in destroy's
// deletion set. Deleting it would delete the Lease of whichever run is holding
// it, including destroy's own.
func operatorNamespace(manifests []byte) (string, error) {
	objs, err := apply.Decode(manifests)
	if err != nil {
		return "", err
	}
	for _, o := range objs {
		if o.GetKind() == "Namespace" {
			if n := o.GetName(); n != "" {
				return n, nil
			}
		}
	}
	return "", errors.New("the rendered operator overlay declares no Namespace, so there is nowhere to take the cluster lock")
}

// ClaimHeldError is the refusal a second operator meets. It carries the facts a
// human needs in order to decide what to do, because "the cluster is locked"
// without a holder and an age is an instruction to guess.
//
// 🔴 IT DOES NOT ASSERT THAT THE HOLDER IS DEAD, and the wording matters as much
// as the mechanism. renewTime was written by another machine's clock and is being
// read by this one; a stale-looking timestamp is evidence, not a verdict. Deciding
// the holder is really gone is Reclaim's job, and it uses a test that does not
// depend on the two clocks agreeing.
type ClaimHeldError struct {
	Instance string
	Holder   string
	Renewed  time.Time
	// LooksStale is the cheap, skew-dependent read, used only to choose which
	// sentence to print.
	LooksStale bool
}

func (e *ClaimHeldError) Error() string {
	ago := "at an unknown time"
	if !e.Renewed.IsZero() {
		ago = fmt.Sprintf("%s ago", time.Since(e.Renewed).Round(time.Second))
	}
	what := "this cluster"
	if e.Instance != "" {
		what = fmt.Sprintf("this cluster (working on instance %q)", e.Instance)
	}
	if e.LooksStale {
		return fmt.Sprintf(
			"%s is claimed by %s, which last renewed %s — by this machine's clock that looks stale, "+
				"but a clock that disagrees looks the same. If that process is genuinely gone, "+
				"take the claim with `dcctl instances reclaim`, which checks properly before it steals",
			what, e.Holder, ago)
	}
	return fmt.Sprintf(
		"%s is being worked on by %s, which renewed %s; wait for it to finish rather than "+
			"running a second apply against the same cluster",
		what, e.Holder, ago)
}

// AcquireClaim takes the cluster lock for the duration of this run.
//
// Create is the whole of the mutual exclusion: two dcctls racing here produce one
// success and one AlreadyExists, decided by the API server. Everything after that
// is about reporting the refusal usefully, not about deciding it.
//
// 🔴 An existing Lease is NEVER taken implicitly, however stale it looks. Acting
// on an inference about a process on a machine we cannot see is exactly the
// outcome a claim exists to prevent — two appliers, one cluster, neither aware of
// the other. Reclaim is a separate, explicit act with a stronger test.
func AcquireClaim(ctx context.Context, client kubernetes.Interface, ns, instance string) (*Claim, error) {
	holder, err := newHolderIdentity()
	if err != nil {
		return nil, err
	}
	secs := int32(claimLeaseDuration / time.Second)
	now := metav1.NewMicroTime(time.Now())

	_, err = client.CoordinationV1().Leases(ns).Create(ctx, &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{
			Name:        claimLeaseName,
			Namespace:   ns,
			Annotations: map[string]string{annotationClaimInstance: instance},
		},
		Spec: coordinationv1.LeaseSpec{
			HolderIdentity:       &holder,
			LeaseDurationSeconds: &secs,
			AcquireTime:          &now,
			RenewTime:            &now,
		},
	}, metav1.CreateOptions{})
	if err == nil {
		return newClaim(client, ns, instance, holder), nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return nil, fmt.Errorf("taking the cluster lock: %w", err)
	}

	existing, gerr := client.CoordinationV1().Leases(ns).Get(ctx, claimLeaseName, metav1.GetOptions{})
	if gerr != nil {
		if apierrors.IsNotFound(gerr) {
			// Released between our Create and our Get. Say so rather than looping:
			// a retry here is how two appliers are produced out of one honest race.
			return nil, errors.New("the cluster lock was released while we were reading it; run the same command again")
		}
		return nil, fmt.Errorf("reading the cluster lock: %w", gerr)
	}
	return nil, heldError(existing)
}

func heldError(l *coordinationv1.Lease) *ClaimHeldError {
	e := &ClaimHeldError{
		Holder:     ptrString(l.Spec.HolderIdentity),
		Instance:   l.Annotations[annotationClaimInstance],
		LooksStale: looksStale(l, time.Now()),
	}
	if l.Spec.RenewTime != nil {
		e.Renewed = l.Spec.RenewTime.Time
	}
	return e
}

// looksStale is the cheap read, and it is ONLY ever used to choose wording.
//
// 🔴 It compares a timestamp written by another machine's clock against this
// one's, so it is wrong by exactly the skew between them — and the dangerous
// direction is the one that calls a live holder dead. Nothing acts on it.
// confirmAbandoned is what decides.
func looksStale(l *coordinationv1.Lease, now time.Time) bool {
	if l.Spec.RenewTime == nil {
		return true
	}
	d := claimLeaseDuration
	if l.Spec.LeaseDurationSeconds != nil && *l.Spec.LeaseDurationSeconds > 0 {
		d = time.Duration(*l.Spec.LeaseDurationSeconds) * time.Second
	}
	return now.After(l.Spec.RenewTime.Add(d))
}

func ptrString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// confirmAbandoned decides whether the holder of the Lease is really gone.
//
// 🔴 THE TEST IS THAT THE OBJECT DID NOT CHANGE OVER A WINDOW THIS MACHINE TIMED,
// and every word of that is load-bearing. It reads resourceVersion — assigned by
// the API server, bumped by any write — and re-reads it a full claimLeaseDuration
// later, measured on the OBSERVER's clock. A live holder renews six times inside
// that window, so an unchanged resourceVersion means no renewal reached the API
// server at all.
//
// Nothing here compares two machines' clocks. renewTime is written by the holder
// and read by the reclaimer, so any comparison between them is wrong by the skew
// between the two, and the dangerous direction — declaring a live holder dead —
// is the one a laptop with a drifting clock produces for free. That is why the
// cheap check is confined to choosing an error message and this is what decides.
//
// It is still not a proof of death: a process stopped (SIGSTOP, a closed lid, a
// suspended VM) for longer than the window also fails to renew. Nothing a second
// machine can observe distinguishes those two, which is precisely why a reclaim is
// explicit, typed, and fenced rather than automatic.
func confirmAbandoned(ctx context.Context, client kubernetes.Interface, ns string, sleep func(context.Context, time.Duration) error) (abandoned bool, l *coordinationv1.Lease, err error) {
	leases := client.CoordinationV1().Leases(ns)
	first, err := leases.Get(ctx, claimLeaseName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil, nil
		}
		return false, nil, err
	}

	if err := sleep(ctx, claimLeaseDuration); err != nil {
		return false, first, err
	}

	second, err := leases.Get(ctx, claimLeaseName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			// The holder exited while we watched and deleted its own Lease. The
			// lock is FREE, which is a different answer from "may be stolen" and
			// leads the caller somewhere different.
			return false, nil, nil
		}
		return false, first, err
	}
	if second.ResourceVersion != first.ResourceVersion {
		return false, second, nil
	}
	return true, second, nil
}

// sleepCtx waits, or returns early if the caller gave up.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func newClaim(client kubernetes.Interface, ns, instance, holder string) *Claim {
	c := &Claim{
		instance: instance,
		ns:       ns,
		holder:   holder,
		client:   client,
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
		lastHeld: time.Now(),
	}
	go c.renewLoop()
	return c
}

// renewLoop keeps the Lease alive and — the part that matters — notices when it
// has stopped being ours.
func (c *Claim) renewLoop() {
	defer close(c.done)
	t := time.NewTicker(claimRenewInterval)
	defer t.Stop()
	for {
		select {
		case <-c.stop:
			return
		case <-t.C:
			if lost := c.renewOnce(); lost != nil {
				c.setLost(lost)
				return
			}
		}
	}
}

// renewOnce re-reads the Lease, checks it is still ours, and extends it. A nil
// return means "carry on", which is not the same as "renewed".
//
// The read is not redundant with the write. It is the fence: a reclaim writes a
// new holder identity, and this comparison is how the original process finds out,
// at most one renewal interval later. Without it a reclaimed process would carry
// on applying, which turns the reclaim into the very thing it was meant to prevent.
//
// 🔴 A RUN THAT CANNOT RENEW FOR A FULL LEASE DURATION DECLARES ITSELF LOST, and
// that symmetry is the point. The reclaimer's test is "nothing changed for a lease
// duration on MY clock"; this is the same window on the holder's own clock. Without
// it, a holder partitioned from the API server keeps applying with total confidence
// while a reclaimer correctly and honestly concludes it is gone — the one
// interleaving where both sides follow the rules and both are wrong.
func (c *Claim) renewOnce() error {
	ctx, cancel := context.WithTimeout(context.Background(), claimRenewInterval)
	defer cancel()

	leases := c.client.CoordinationV1().Leases(c.ns)
	cur, err := leases.Get(ctx, claimLeaseName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			// Deleted out from under us. Never recreate it: a holder that
			// recreates its own Lease on NotFound is a second holder the moment
			// somebody else has already taken it.
			return fmt.Errorf("%w: the cluster lock was deleted", ErrClaimLost)
		}
		return c.staleness()
	}
	if got := ptrString(cur.Spec.HolderIdentity); got != c.holder {
		return fmt.Errorf("%w: it is now held by %s", ErrClaimLost, got)
	}

	now := metav1.NewMicroTime(time.Now())
	cur.Spec.RenewTime = &now
	// Update, carrying the resourceVersion the Get returned — never Patch. A Patch
	// would write our holder identity over whatever is there, so a reclaimed
	// process would silently steal the lock BACK from the operator who took it.
	if _, err := leases.Update(ctx, cur, metav1.UpdateOptions{}); err != nil {
		return c.staleness()
	}

	c.mu.Lock()
	c.lastHeld = time.Now()
	c.mu.Unlock()
	return nil
}

// staleness reports lost once this run has gone a full lease duration with no
// successful renewal, and nil before that — so a few failures in a row are
// survivable, which is what the 6:1 interval ratio is for.
func (c *Claim) staleness() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if since := time.Since(c.lastHeld); since > claimLeaseDuration {
		return fmt.Errorf("%w: this run has not renewed the cluster lock for %s, so another operator "+
			"may already have reclaimed it", ErrClaimLost, since.Round(time.Second))
	}
	return nil
}

func (c *Claim) setLost(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lost == nil {
		c.lost = err
	}
}

// CheckHeld is the fence, and it runs at every step boundary.
//
// 🔴 IT ASKS THE API SERVER RATHER THAN READING A FLAG. The renewal goroutine sets
// that flag on a 10s ticker, and a step can finish and the next one begin inside
// that window — so a cached answer can be a full interval out of date at exactly
// the moment it is consulted. One extra GET per step boundary, eight per run, is
// not a cost worth trading a stale answer for.
//
// A transient API failure falls back to what the renewal loop knows. That is
// deliberate in both directions: it does not abort a healthy run over one failed
// call, and it cannot mask a genuine loss, because a run that has been unable to
// renew for a full lease duration has already declared itself lost (staleness).
//
// 🔑 WHAT IT CANNOT DO is stop a step already in flight. The bound on the exposure
// is the remainder of the CURRENT STEP — see Pipeline.Run, where the honest size
// of that window is written down rather than rounded off to the renewal interval.
func (c *Claim) CheckHeld(ctx context.Context) error {
	if cached := c.cachedLoss(); cached != nil {
		return cached
	}
	cur, err := c.client.CoordinationV1().Leases(c.ns).Get(ctx, claimLeaseName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			lost := fmt.Errorf("%w: the cluster lock was deleted", ErrClaimLost)
			c.setLost(lost)
			return lost
		}
		return c.cachedLoss()
	}
	if got := ptrString(cur.Spec.HolderIdentity); got != c.holder {
		lost := fmt.Errorf("%w: it is now held by %s", ErrClaimLost, got)
		c.setLost(lost)
		return lost
	}
	return nil
}

func (c *Claim) cachedLoss() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lost
}

// Lost reports whether this run has already been fenced. Callers use it to stay
// silent: a fenced run must not write anything further to the cluster, the phase
// annotation included, because the object it would be writing to now belongs to
// whoever reclaimed it.
func (c *Claim) Lost() bool { return c.cachedLoss() != nil }

// Release gives the claim up.
//
// The Lease is DELETED rather than left to expire, because a CLI that has exited
// holds nothing. Leaving it behind would send the next run — most often the same
// operator retrying — down the reclaim path, which costs a full lease duration of
// waiting and a typed confirmation, to answer a question the exiting process
// already knew the answer to.
//
// A Lease that is no longer ours is left exactly where it is. Deleting it would
// destroy the claim of whoever reclaimed it, which is the one thing a losing
// process must never do on its way out.
func (c *Claim) Release(ctx context.Context) {
	c.stopOnce.Do(func() { close(c.stop) })
	<-c.done

	leases := c.client.CoordinationV1().Leases(c.ns)
	cur, err := leases.Get(ctx, claimLeaseName, metav1.GetOptions{})
	if err != nil || ptrString(cur.Spec.HolderIdentity) != c.holder {
		return
	}
	uid := cur.UID
	rv := cur.ResourceVersion
	_ = leases.Delete(ctx, claimLeaseName, metav1.DeleteOptions{
		Preconditions: &metav1.Preconditions{UID: &uid, ResourceVersion: &rv},
	})
}

// Reclaim takes an abandoned cluster lock for a new holder.
//
// It refuses unless confirmAbandoned says nothing has touched the Lease for a full
// lease duration, and it writes a fresh nonce so the previous holder's own renewal
// loop discovers the loss within one interval. The Update carries the
// resourceVersion from the confirming read, so a holder that wakes up and renews
// between the confirmation and the steal wins — the reclaim fails with a Conflict
// instead of silently overwriting a live claim.
func Reclaim(ctx context.Context, client kubernetes.Interface, ns string) (*Claim, error) {
	abandoned, existing, err := confirmAbandoned(ctx, client, ns, sleepCtx)
	if err != nil {
		return nil, fmt.Errorf("checking the cluster lock: %w", err)
	}
	if existing == nil {
		return nil, errors.New("this cluster is not claimed; there is nothing to reclaim")
	}
	if !abandoned {
		return nil, heldError(existing)
	}

	holder, err := newHolderIdentity()
	if err != nil {
		return nil, err
	}
	instance := existing.Annotations[annotationClaimInstance]
	secs := int32(claimLeaseDuration / time.Second)
	now := metav1.NewMicroTime(time.Now())
	existing.Spec.HolderIdentity = &holder
	existing.Spec.LeaseDurationSeconds = &secs
	existing.Spec.AcquireTime = &now
	existing.Spec.RenewTime = &now
	if existing.Spec.LeaseTransitions == nil {
		var zero int32
		existing.Spec.LeaseTransitions = &zero
	}
	*existing.Spec.LeaseTransitions++

	if _, err := client.CoordinationV1().Leases(ns).Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
		if apierrors.IsConflict(err) {
			return nil, errors.New("the cluster lock changed while we were confirming it was abandoned, " +
				"which means the previous holder is alive — the reclaim was refused")
		}
		return nil, fmt.Errorf("reclaiming the cluster lock: %w", err)
	}
	return newClaim(client, ns, instance, holder), nil
}

// PeekClaim reports the current lock without taking it, for `instances list` and
// for the reclaim command's confirmation prompt. A nil Lease means the lock is free.
func PeekClaim(ctx context.Context, client kubernetes.Interface, ns string) (*coordinationv1.Lease, error) {
	l, err := client.CoordinationV1().Leases(ns).Get(ctx, claimLeaseName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return l, nil
}

// ClaimLeaseDuration is how long a lock survives without renewal, exported so a
// command can tell the operator how long its check is going to take before it
// starts rather than after.
func ClaimLeaseDuration() time.Duration { return claimLeaseDuration }

// ClaimClients resolves where the cluster lock lives and how to reach it, for the
// commands that act on the lock without running a pipeline.
//
// The namespace is read from the operator overlay, the same source stepInstallCore
// uses, rather than from a constant — see operatorNamespace. The overlay renders
// with no image because nothing here is going to apply it; only its namespace is
// wanted, and that is a kustomize setting rather than a property of the build.
func ClaimClients(kubeContext string) (string, kubernetes.Interface, error) {
	manifests, err := dck8s.RenderOperator("")
	if err != nil {
		return "", nil, fmt.Errorf("rendering the operator overlay to find where the cluster lock lives: %w", err)
	}
	ns, err := operatorNamespace(manifests)
	if err != nil {
		return "", nil, err
	}
	_, _, typed, err := kubeClients(kubeContext)
	if err != nil {
		return "", nil, fmt.Errorf("connecting to the cluster: %w", err)
	}
	return ns, typed, nil
}
