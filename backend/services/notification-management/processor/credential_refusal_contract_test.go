// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/httpsink"
	"github.com/devicechain-io/dc-notification-management/model"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// The refusals carry httpsink's sentinels, which is what deliverWithRetry classifies on. A
// refusal that stopped wrapping them would fall into the retry branch.
func TestWebhookCredentialRefusalsCarryTheirSentinel(t *testing.T) {
	srv, _, _ := hitCountingServer(t)
	for _, c := range []struct {
		config, secret string
		want           error
	}{
		{`{"url":"` + srv.URL + `","auth":"bearer"}`, "", httpsink.ErrMissingCredential},
		{`{"url":"` + srv.URL + `","auth":"header","authHeader":"X-API-Key"}`, "", httpsink.ErrMissingCredential},
		{`{"url":"` + srv.URL + `","auth":"none"}`, "s", httpsink.ErrUnexpectedCredential},
		{`{"url":"` + srv.URL + `"}`, "", httpsink.ErrAuthModeUnstated},
	} {
		err := deliverWebhook(t, srv, c.config, c.secret)
		if !errors.Is(err, c.want) {
			t.Errorf("config %s secret %q: err = %v, want %v", c.config, c.secret, err, c.want)
		}
	}
}

func TestSMTPCredentialRefusalCarriesTheSharedSentinel(t *testing.T) {
	port, _ := silentSMTPListener(t)
	// Bounded: the listener never greets, so an adapter that dialled before checking the
	// credential would otherwise block here with no deadline and hang the whole package,
	// masking the sibling tests that name that defect.
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	err := loopbackSMTPAdapter(t).Deliver(ctx, smtpUsernameOnlyChannel(port), "",
		[]string{"ops@x.invalid"}, &RenderedNotification{Subject: "s", TextBody: "b"})
	if !errors.Is(err, httpsink.ErrMissingCredential) {
		t.Fatalf("err = %v, want httpsink.ErrMissingCredential", err)
	}
}

func counterValue(t *testing.T, vec *prometheus.CounterVec, label string) float64 {
	t.Helper()
	m := &dto.Metric{}
	if err := vec.WithLabelValues(label).Write(m); err != nil {
		t.Fatalf("read counter: %v", err)
	}
	return m.GetCounter().GetValue()
}

// A refused delivery is acked — not redelivered, not dead-lettered — so the counter is the
// only thing an alert can see. It is built through the real constructor, so the wiring from
// NotifyMetrics to the notifier is what is under test, not a field set by hand.
func TestARefusedDeliveryIsCountedByReason(t *testing.T) {
	refused := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "refused_test"}, []string{"reason"})
	n := NewPolicyNotifier(nil, nil, 3, 2*time.Second, nil, nil, NotifyMetrics{deliveriesRefused: refused})

	srv, _, _ := hitCountingServer(t)
	n.adapters = map[string]ChannelAdapter{
		model.ChannelTypeWebhook: &webhookAdapter{client: srv.Client()},
		model.ChannelTypeSMTP:    &blockingAdapter{},
	}
	credential := delivery{channel: channelWith("hook", model.ChannelTypeWebhook, `{"url":"`+srv.URL+`","auth":"bearer"}`)}
	blocked := delivery{channel: channelWith("mail", model.ChannelTypeSMTP, ""), recipients: []string{"x@x.invalid"}}

	if got := n.deliverWithRetry(context.Background(), credential, &RenderedNotification{Payload: map[string]any{}}); got != deliveryRefused {
		t.Fatalf("credential delivery = %v, want deliveryRefused", got)
	}
	if got := n.deliverWithRetry(context.Background(), blocked, &RenderedNotification{}); got != deliveryRefused {
		t.Fatalf("blocked delivery = %v, want deliveryRefused", got)
	}
	if got := counterValue(t, refused, refusalReasonCredential); got != 1 {
		t.Errorf("credential refusals counted = %v, want 1", got)
	}
	if got := counterValue(t, refused, refusalReasonEgress); got != 1 {
		t.Errorf("egress refusals counted = %v, want 1", got)
	}
}

// The counterweight: a transient failure is not a refusal, and is not counted as one.
func TestATransientFailureIsNotCountedAsARefusal(t *testing.T) {
	refused := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "refused_test"}, []string{"reason"})
	n := NewPolicyNotifier(nil, nil, 2, 50*time.Millisecond, nil, nil, NotifyMetrics{deliveriesRefused: refused})
	n.adapters = map[string]ChannelAdapter{model.ChannelTypeSMTP: &fakeAdapter{failTimes: 99}}
	d := delivery{channel: channelWith("mail", model.ChannelTypeSMTP, ""), recipients: []string{"x@x.invalid"}}

	if got := n.deliverWithRetry(context.Background(), d, &RenderedNotification{}); got != deliveryFailed {
		t.Fatalf("deliverWithRetry = %v, want deliveryFailed", got)
	}
	for _, reason := range []string{refusalReasonCredential, refusalReasonEgress} {
		if got := counterValue(t, refused, reason); got != 0 {
			t.Errorf("%s refusals counted = %v, want 0", reason, got)
		}
	}
}

// The series operators are told to alert on is built by NewNotifyMetrics, under the
// documented name, and a credential refusal moves it. The tests above hand the notifier a
// counter built here; this one builds it the way main.go does, so a constructor that stopped
// building the counter (countRefusal tolerates nil silently) or renamed it fails here.
func TestARefusalMovesTheDocumentedSeries(t *testing.T) {
	ms := &core.Microservice{InstanceId: "test", FunctionalArea: "notification-management"}
	// Without a registry core builds metrics unregistered, and Gather would see nothing.
	reg := prometheus.NewRegistry()
	ms.UseMetricsRegistry(reg)
	n := NewPolicyNotifier(nil, nil, 3, 2*time.Second, nil, nil, NewNotifyMetrics(ms))

	srv, _, _ := hitCountingServer(t)
	n.adapters = map[string]ChannelAdapter{model.ChannelTypeWebhook: &webhookAdapter{client: srv.Client()}}
	d := delivery{channel: channelWith("hook", model.ChannelTypeWebhook, `{"url":"`+srv.URL+`","auth":"bearer"}`)}
	if got := n.deliverWithRetry(context.Background(), d, &RenderedNotification{Payload: map[string]any{}}); got != deliveryRefused {
		t.Fatalf("deliverWithRetry = %v, want deliveryRefused", got)
	}

	const want = "devicechain_notificationmanagement_deliveries_refused_total"
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gathering the registry: %v", err)
	}
	for _, f := range families {
		if f.GetName() != want {
			continue
		}
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "reason" && l.GetValue() == refusalReasonCredential {
					if v := m.GetCounter().GetValue(); v != 1 {
						t.Fatalf("%s{reason=%q} = %v, want 1", want, refusalReasonCredential, v)
					}
					return
				}
			}
		}
		t.Fatalf("%s has no reason=%q series", want, refusalReasonCredential)
	}
	t.Fatalf("the registry holds no %q", want)
}
