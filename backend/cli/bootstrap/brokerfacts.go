// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/entityevent"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"k8s.io/client-go/kubernetes"
)

// `dcctl broker facts` answers a support question nothing else on the operator plane can: was the
// deletion of an entity ANNOUNCED to the services that hold references to it, and when? The answer
// is on the broker, in the entity-deleted stream device-management publishes from its outbox, and
// this reads it there as the broker administrator, one stored message at a time.
//
// 🔑 IT READS AND NOTHING ELSE. The administrator is granted STREAM.MSG.GET and DIRECT.GET on the
// streams the table declares OperatorReadable (core/natsauth), and no purge, message delete or
// consumer: a deletion fact the operator plane could remove would be a fact nobody could rely on.
// The reads are by sequence (and subject), so nothing is created on the broker either.

// factReadTimeout bounds one stored-message read. A variable so a test can shorten it.
var factReadTimeout = 10 * time.Second

// BrokerFactsOptions are the options of BrokerFacts.
type BrokerFactsOptions struct {
	Options
	// Tenant limits the read to one tenant's facts (by subject, on the broker). Empty is every tenant.
	Tenant string
	// Token limits the output to the facts about the entity with this token.
	Token string
	// Since limits the output to the facts the broker stored within this long before now. Zero is
	// every fact the stream still holds.
	Since time.Duration
	// Out is where the table is written.
	Out io.Writer
}

// DeletionFact is one deletion fact as the broker stores it.
type DeletionFact struct {
	Seq    uint64
	Stored time.Time
	Tenant string
	// MsgID is the Nats-Msg-Id the outbox published under (entdel/<tenant>/<type>/<id>): the only
	// header read, because it is what dedupes a re-publish, and so what an operator matches on.
	MsgID       string
	EntityType  string
	EntityID    uint
	EntityToken string
	Deleted     time.Time
	// Undecodable is why the payload could not be read as a deletion fact, when it could not.
	Undecodable string
}

// factFilter is what to read.
type factFilter struct {
	tenant string
	token  string
	since  time.Time // zero: no lower bound
}

// factStream is the part of a JetStream stream the read uses: its bounds, and one stored message
// by sequence, optionally the next one at or after it on a subject.
type factStream interface {
	CachedInfo() *jetstream.StreamInfo
	GetMsg(ctx context.Context, seq uint64, opts ...jetstream.GetMsgOpt) (*jetstream.RawStreamMsg, error)
}

// BrokerFacts reads the deletion facts of an installed instance through a port-forward to a broker
// pod, as the broker administrator, and prints them.
func BrokerFacts(ctx context.Context, opts BrokerFactsOptions) error {
	if opts.Tenant != "" {
		if err := core.ValidateToken(opts.Tenant); err != nil {
			return fmt.Errorf("--tenant: %w", err)
		}
	}
	if opts.Since < 0 {
		return fmt.Errorf("--since must not be negative")
	}
	binding, source := ResolveBinding(opts.Options)
	if err := refuseUnreadable(source, opts.Instance); err != nil {
		return err
	}
	if err := RefuseConflictingContext(source, binding, opts.KubeContext, opts.Instance); err != nil {
		return err
	}
	announceBinding(binding, source, opts.Instance)
	restCfg, err := RestConfig(binding.KubeContext)
	if err != nil {
		return err
	}
	typed, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return err
	}
	cfg, err := deployedInstanceConfigWithLogins(ctx, typed, opts.Instance)
	if err != nil {
		return err
	}
	ns := InstanceNamespace(opts.Instance)
	pods, err := natsPods(ctx, typed, ns)
	if err != nil {
		return err
	}
	if len(pods) == 0 {
		return fmt.Errorf("no broker pod matched %q in namespace %s", natsPodSelector, ns)
	}
	local, stop, err := forwardPortCtx(ctx, restCfg, ns, pods[0].Name, int(cfg.Infrastructure.Nats.Port))
	if err != nil {
		return err
	}
	defer stop()
	nc, err := dialBrokerCtx(ctx, fmt.Sprintf("nats://127.0.0.1:%d", local), cfg.Infrastructure.Nats)
	if err != nil {
		return err
	}
	defer nc.Close()
	filter := factFilter{tenant: opts.Tenant, token: opts.Token}
	if opts.Since > 0 {
		filter.since = time.Now().Add(-opts.Since)
	}
	facts, info, err := readBrokerFacts(ctx, nc, opts.Instance, filter)
	if err != nil {
		return err
	}
	printBrokerFacts(opts.Out, info, facts)
	return nil
}

// readBrokerFacts opens the instance's entity-deleted stream over nc and reads the facts filter
// selects.
func readBrokerFacts(ctx context.Context, nc *nats.Conn, instanceId string, filter factFilter) ([]DeletionFact, *jetstream.StreamInfo, error) {
	js, err := jetstream.New(nc)
	if err != nil {
		return nil, nil, err
	}
	name := messaging.StreamName(instanceId, streams.EntityDeleted)
	rctx, cancel := context.WithTimeout(ctx, factReadTimeout)
	stream, err := js.Stream(rctx, name)
	cancel()
	if err != nil {
		if errors.Is(err, jetstream.ErrStreamNotFound) {
			return nil, nil, fmt.Errorf("stream %s does not exist: the instance is not running device-management, or it is not "+
				"the instance it was taken for", name)
		}
		return nil, nil, fmt.Errorf("reading stream %s: %w", name, err)
	}
	facts, err := collectFacts(ctx, stream, instanceId, filter)
	return facts, stream.CachedInfo(), err
}

// collectFacts walks the stream from its first sequence, on the tenant's subject when one is
// given, and keeps the facts the filter selects.
func collectFacts(ctx context.Context, stream factStream, instanceId string, filter factFilter) ([]DeletionFact, error) {
	info := stream.CachedInfo()
	name := messaging.StreamName(instanceId, streams.EntityDeleted)
	subject := messaging.StreamSubject(instanceId, streams.EntityDeleted)
	if filter.tenant != "" {
		subject = messaging.TenantSubjectFilter(instanceId, filter.tenant, streams.EntityDeleted)
	}
	var out []DeletionFact
	if info == nil || info.State.Msgs == 0 {
		return out, nil
	}
	for seq := max(info.State.FirstSeq, 1); seq <= info.State.LastSeq; {
		rctx, cancel := context.WithTimeout(ctx, factReadTimeout)
		m, err := stream.GetMsg(rctx, seq, jetstream.WithGetMsgSubject(subject))
		cancel()
		if err != nil {
			if errors.Is(err, jetstream.ErrMsgNotFound) {
				break // nothing on the subject at or after seq
			}
			if ctx.Err() != nil {
				return out, ctx.Err()
			}
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, nats.ErrTimeout) || errors.Is(err, nats.ErrNoResponders) ||
				strings.Contains(strings.ToLower(err.Error()), "permissions violation") {
				// The broker drops, without answering, a request its login may not make, so a denied
				// read looks exactly like a read that timed out.
				return out, fmt.Errorf("reading %s message %d was denied or went unanswered (%v): the broker administrator "+
					"login most likely lacks $JS.API.STREAM.MSG.GET.%s, which an instance gains when it is upgraded to a "+
					"release that has this command (dcctl upgrade); the broker log shows a Permissions Violation if so",
					name, seq, err, name)
			}
			return out, fmt.Errorf("reading %s message %d: %w", name, seq, err)
		}
		seq = m.Sequence + 1
		f := decodeFact(instanceId, m)
		if filter.token != "" && f.EntityToken != filter.token {
			continue
		}
		if !filter.since.IsZero() && f.Stored.Before(filter.since) {
			continue
		}
		out = append(out, f)
	}
	return out, nil
}

// decodeFact reads one stored message as a deletion fact. The tenant is the subject's, since the
// payload does not carry it; of the headers only the Nats-Msg-Id is read.
func decodeFact(instanceId string, m *jetstream.RawStreamMsg) DeletionFact {
	f := DeletionFact{Seq: m.Sequence, Stored: m.Time, Tenant: tenantOfFactSubject(instanceId, m.Subject)}
	if m.Header != nil {
		f.MsgID = m.Header.Get(nats.MsgIdHdr)
	}
	ev, err := entityevent.UnmarshalEntityDeletedEvent(m.Data)
	if err != nil {
		f.Undecodable = err.Error()
		return f
	}
	f.EntityType, f.EntityID, f.EntityToken, f.Deleted = string(ev.EntityType), ev.EntityId, ev.EntityToken, ev.DeletedTime
	return f
}

// tenantOfFactSubject is the tenant level of "<instance>.<tenant>.entity-deleted", or "?" for a
// subject of any other shape (which the stream's own subject filter makes impossible).
func tenantOfFactSubject(instanceId, subject string) string {
	parts := strings.Split(subject, ".")
	if len(parts) == 3 && parts[0] == instanceId && parts[2] == streams.EntityDeleted && parts[1] != "" {
		return parts[1]
	}
	return "?"
}

// printBrokerFacts writes the facts as a table, with a line saying what the stream holds, so an
// empty table is never mistaken for an empty stream.
func printBrokerFacts(w io.Writer, info *jetstream.StreamInfo, facts []DeletionFact) {
	if info != nil {
		fmt.Fprintf(w, "stream %s holds %d message(s), sequences %d to %d\n",
			info.Config.Name, info.State.Msgs, info.State.FirstSeq, info.State.LastSeq)
	}
	if len(facts) == 0 {
		fmt.Fprintln(w, "no deletion fact matched")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SEQ\tSTORED\tTENANT\tTYPE\tID\tTOKEN\tDELETED\tMSG-ID")
	for _, f := range facts {
		stored := f.Stored.UTC().Format(time.RFC3339Nano)
		if f.Undecodable != "" {
			fmt.Fprintf(tw, "%d\t%s\t%s\t(undecodable: %s)\t\t\t\t%s\n", f.Seq, stored, f.Tenant, f.Undecodable, f.MsgID)
			continue
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", f.Seq, stored, f.Tenant, f.EntityType,
			strconv.FormatUint(uint64(f.EntityID), 10), f.EntityToken, f.Deleted.UTC().Format(time.RFC3339Nano), f.MsgID)
	}
	_ = tw.Flush()
	fmt.Fprintf(w, "%d deletion fact(s)\n", len(facts))
}
