// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package scenariosink

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/smtp"
	"strings"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// Each endpoint is driven by a REAL client of its protocol — paho, the client the platform's
// outbound connector uses, and net/smtp, the one its mail channel uses — and the record must hold
// exactly what was sent.
func TestTheSinkRecordsWhatRealClientsDeliver(t *testing.T) {
	s := New()
	web := httptest.NewServer(s)
	defer web.Close()

	resp, err := http.Post(web.URL+"/hooks/flood", "application/json", strings.NewReader(`{"device":"g1"}`))
	if err != nil || resp.StatusCode != http.StatusNoContent {
		t.Fatalf("webhook: %v %v", resp, err)
	}

	ml, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ml.Close()
	go func() { _ = s.ServeMQTT(ml) }()
	opts := mqtt.NewClientOptions().AddBroker("tcp://" + ml.Addr().String()).SetClientID("t").SetConnectTimeout(5 * time.Second)
	client := mqtt.NewClient(opts)
	if tok := client.Connect(); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		t.Fatalf("mqtt connect: %v", tok.Error())
	}
	for _, qos := range []byte{0, 1, 2} {
		if tok := client.Publish("harborpulse/t1/flood-bulletin", qos, false, `{"device":"g2"}`); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
			t.Fatalf("mqtt publish qos %d: %v", qos, tok.Error())
		}
	}
	client.Disconnect(100)

	sl, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer sl.Close()
	go func() { _ = s.ServeSMTP(sl) }()
	msg := "Subject: flood\r\n\r\n.dotted line\r\nbody\r\n"
	if err := smtp.SendMail(sl.Addr().String(), nil, "a@x.example", []string{"duty-t1@port-alder.example"}, []byte(msg)); err != nil {
		t.Fatalf("smtp: %v", err)
	}

	rec, err := Fetch(http.DefaultClient, web.URL)
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.Webhooks) != 1 || rec.Webhooks[0].Path != "/hooks/flood" || rec.Webhooks[0].Body != `{"device":"g1"}` ||
		rec.Webhooks[0].Headers["Content-Type"][0] != "application/json" {
		t.Errorf("webhooks %+v", rec.Webhooks)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(s.Snapshot().Messages) < 3 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	rec = s.Snapshot()
	if len(rec.Messages) != 3 {
		t.Fatalf("messages %+v", rec.Messages)
	}
	for i, m := range rec.Messages {
		if m.Topic != "harborpulse/t1/flood-bulletin" || m.Payload != `{"device":"g2"}` || m.QoS != i {
			t.Errorf("message %d: %+v", i, m)
		}
	}
	if len(rec.Mail) != 1 || rec.Mail[0].To[0] != "duty-t1@port-alder.example" || rec.Mail[0].From != "a@x.example" ||
		!strings.Contains(rec.Mail[0].Data, "\n.dotted line\n") {
		t.Errorf("mail %+v", rec.Mail)
	}
}

func TestTheRecordIsBounded(t *testing.T) {
	s := New()
	for i := 0; i < MaxRecords+5; i++ {
		s.mqttPublish(0x30, []byte{0, 1, 't', 'p'})
	}
	rec := s.Snapshot()
	if len(rec.Messages) != MaxRecords || rec.Dropped != 5 {
		t.Fatalf("%d messages, %d dropped", len(rec.Messages), rec.Dropped)
	}
}
