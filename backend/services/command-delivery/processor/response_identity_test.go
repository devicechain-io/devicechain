// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/devicechain-io/dc-command-delivery/model"
	"github.com/devicechain-io/dc-microservice/deadletter"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// counterValue reads a counter's current value.
func counterValue(t *testing.T, c prometheus.Counter) float64 {
	t.Helper()
	var m dto.Metric
	if err := c.Write(&m); err != nil {
		t.Fatalf("reading counter: %v", err)
	}
	return m.GetCounter().GetValue()
}

// oneMessageReader hands ProcessMessage a single message and then EOF, so one call to
// ProcessMessage consumes exactly one message and the test controls the subject verbatim.
type oneMessageReader struct {
	msg  messaging.Message
	done bool
}

func (r *oneMessageReader) ReadMessage(context.Context) (messaging.Message, error) {
	if r.done {
		return messaging.Message{}, io.EOF
	}
	r.done = true
	return r.msg, nil
}
func (r *oneMessageReader) HandleResponse(error) {}

func responseProcessor(t *testing.T, api *fakeApi, subject string, body string) *CommandDeliveryProcessor {
	t.Helper()
	return &CommandDeliveryProcessor{
		Api: api,
		CommandResponsesReader: &oneMessageReader{
			msg: messaging.Message{Subject: subject, Value: []byte(body)},
		},
	}
}

// TestProcessMessageTakesTheResponderFromTheSubject is the heart of the fix on the
// consumer side.
//
// 🔴 THE PAYLOAD DELIBERATELY CARRIES A DIFFERENT DEVICE, and that is the whole test.
// The response body here claims to be pump-9 while the broker-verified subject says
// pump-1. A device chooses its payload and cannot choose its subject — its signed grant
// permits publishing only to its own — so the subject is the only field a forger does not
// control. A processor that read the body would hand command-delivery an identity the
// attacker picked, and every downstream check would then be verifying the attacker's own
// claim against itself.
func TestProcessMessageTakesTheResponderFromTheSubject(t *testing.T) {
	api := &fakeApi{}
	body := `{"commandToken":"cmd-1","success":true,"deviceToken":"pump-9"}`
	p := responseProcessor(t, api, "inst-1.acme.command-responses.pump-1", body)

	p.ProcessMessage(context.Background())

	if len(api.responseCalls) != 1 {
		t.Fatalf("MarkResponse called %d times, want 1", len(api.responseCalls))
	}
	got := api.responseCalls[0]
	if got.responder != "pump-1" {
		t.Fatalf("responder = %q, want pump-1 (from the subject); the payload's claim of "+
			"pump-9 must not be read", got.responder)
	}
	if got.commandToken != "cmd-1" {
		t.Fatalf("commandToken = %q, want cmd-1", got.commandToken)
	}
}

// A subject carrying no device identity must be refused outright rather than processed
// anonymously — including the OLD tenant-wide shape, which is what a device built against
// the previous topic still publishes to.
func TestProcessMessageRefusesASubjectWithNoDevice(t *testing.T) {
	for name, subject := range map[string]string{
		"the old tenant-wide subject": "inst-1.acme.command-responses",
		"no tenant at all":            "command-responses",
		"an extra segment":            "inst-1.acme.command-responses.pump-1.extra",
	} {
		t.Run(name, func(t *testing.T) {
			api := &fakeApi{}
			p := responseProcessor(t, api, subject, `{"commandToken":"cmd-1","success":true}`)

			p.ProcessMessage(context.Background())

			if len(api.responseCalls) != 0 {
				t.Fatalf("a response on %q reached MarkResponse as %+v; a subject with no "+
					"device identity must not be processed at all", subject, api.responseCalls)
			}
		})
	}
}

// A refused response is TERMINAL for the message, not transient. This asserts the
// processor asks once and does not treat the refusal as something to retry.
//
// 🔑 IT IS THE DISPOSITION THAT MATTERS, NOT THE REFUSAL. command-delivery already
// refuses the write; if the processor read that as a transient persist failure it would
// leave the message unacked and redeliver it until MaxDeliver — turning one forged
// response into a retry storm against the database, and burning the delivery budget the
// real responses share.
func TestProcessMessageDoesNotRetryARefusedResponse(t *testing.T) {
	api := &fakeApi{responseErr: model.ErrResponderNotCommandOwner}
	p := responseProcessor(t, api, "inst-1.acme.command-responses.pump-9",
		`{"commandToken":"cmd-1","success":true}`)
	refused := prometheus.NewCounter(prometheus.CounterOpts{Name: "refused_total"})
	p.ResponsesRefused = refused

	if stop := p.ProcessMessage(context.Background()); stop {
		t.Fatal("a refused response must not stop the consumer loop")
	}

	// 🔴 THE COUNT IS THE POINT. This message is labelled "invalid" by the shared result
	// vocabulary, indistinguishable there from an undecodable payload — so if the refusal
	// raises nothing of its own, a device forging outcomes for a whole tenant is a number
	// an operator cannot tell apart from one device with bad firmware.
	if got := counterValue(t, refused); got != 1 {
		t.Fatalf("ResponsesRefused = %v, want 1; the refusal is invisible in metrics", got)
	}
	if len(api.responseCalls) != 1 {
		t.Fatalf("MarkResponse called %d times, want exactly 1", len(api.responseCalls))
	}
	if !errors.Is(api.responseErr, model.ErrResponderNotCommandOwner) {
		t.Fatal("premise lost: the fake must be refusing with the sentinel")
	}
}

// deadRecorder captures what the arm writes so a test can read the letter back.
type deadRecorder struct {
	msgs []messaging.Message
}

func (d *deadRecorder) WriteMessages(_ context.Context, msgs ...messaging.Message) error {
	d.msgs = append(d.msgs, msgs...)
	return nil
}

// responseProcessorAtCap builds a processor whose one message is on its final delivery.
func responseProcessorAtCap(t *testing.T, api *fakeApi, dead *deadRecorder, numDelivered int) *CommandDeliveryProcessor {
	t.Helper()
	return &CommandDeliveryProcessor{
		Api:  api,
		area: "command-delivery",
		dead: deadletter.NewSink(dead, func(error) {}),
		CommandResponsesReader: &oneMessageReader{
			msg: messaging.NewConsumedMessage("inst-1.acme.command-responses.pump-1",
				[]byte(`{"commandToken":"cmd-1","success":true}`), numDelivered, nil, nil),
		},
	}
}

// 🔴 THE ARM. A device's answer that could not be recorded used to end as a log line,
// leaving its command looking unanswered with nothing anywhere saying the device had in
// fact replied.
func TestACommandResponseThatCannotBeRecordedIsDeadLettered(t *testing.T) {
	api := &fakeApi{responseErr: errors.New("the database is away")}
	dead := &deadRecorder{}
	p := responseProcessorAtCap(t, api, dead, messaging.MaxDeliver)

	p.ProcessMessage(context.Background())

	if len(dead.msgs) != 1 {
		t.Fatalf("wrote %d dead letters at the cap, want 1", len(dead.msgs))
	}
	e, err := deadletter.Unmarshal(dead.msgs[0].Value)
	if err != nil {
		t.Fatalf("the written letter does not read back: %v", err)
	}
	if e.Kind != deadletter.KindCommandResponse {
		t.Fatalf("kind = %q", e.Kind)
	}
	if e.Reference != "cmd-1" {
		t.Fatalf("the letter must name the command that still looks unanswered: %q", e.Reference)
	}
	if e.Attempts != messaging.MaxDeliver || e.Subject == "" || len(e.Payload) == 0 {
		t.Fatalf("the letter cannot be located or understood: %+v", e)
	}
}

// 🔴 AND NOT BELOW THE CAP — a response still being retried has not been given up on.
func TestACommandResponseBelowTheCapIsNotDeadLettered(t *testing.T) {
	api := &fakeApi{responseErr: errors.New("the database is away")}
	dead := &deadRecorder{}
	p := responseProcessorAtCap(t, api, dead, 1)

	p.ProcessMessage(context.Background())

	if len(dead.msgs) != 0 {
		t.Fatalf("wrote %d dead letters below the cap, want 0", len(dead.msgs))
	}
}

// 🔑 A REFUSED RESPONSE IS NOT DEAD-LETTERED, and the distinction is the point of having
// two dispositions. A response from a device that does not own the command is not work the
// platform accepted and failed to finish — it is a claim it declined, and recording it
// would file another device's message as that command's answer.
func TestARefusedResponseIsNotDeadLettered(t *testing.T) {
	api := &fakeApi{responseErr: model.ErrResponderNotCommandOwner}
	dead := &deadRecorder{}
	p := responseProcessorAtCap(t, api, dead, messaging.MaxDeliver)
	p.ResponsesRefused = prometheus.NewCounter(prometheus.CounterOpts{Name: "refused2_total"})

	p.ProcessMessage(context.Background())

	if len(dead.msgs) != 0 {
		t.Fatal("a response the platform declined was filed as one it failed to finish")
	}
	if got := counterValue(t, p.ResponsesRefused); got != 1 {
		t.Fatalf("ResponsesRefused = %v, want 1 (premise lost)", got)
	}
}
