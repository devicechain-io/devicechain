// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package mqttack

import (
	"net"
	"sync"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/eclipse/paho.mqtt.golang/packets"
)

// answer is what a fake broker's connection does with the first PUBLISH it reads.
type answer int

const (
	closeIt  answer = iota // close the connection, with no PUBACK
	pubackIt               // PUBACK it
	holdIt                 // neither: keep the connection open and say nothing
)

// fakeBroker accepts MQTT connections one at a time. Each one is CONNACKed with its session
// present, and then does the next of plan with the first PUBLISH it reads; connections past the
// plan are held open, unanswered.
func fakeBroker(t *testing.T, plan ...answer) (string, <-chan *packets.PublishPacket) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var conns []net.Conn
	t.Cleanup(func() {
		_ = ln.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			_ = c.Close()
		}
	})
	got := make(chan *packets.PublishPacket, 16)
	go func() {
		for i := 0; ; i++ {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, conn)
			mu.Unlock()
			a := holdIt
			if i < len(plan) {
				a = plan[i]
			}
			go serve(conn, a, got)
		}
	}()
	return "tcp://" + ln.Addr().String(), got
}

func serve(conn net.Conn, a answer, got chan<- *packets.PublishPacket) {
	if _, err := packets.ReadPacket(conn); err != nil { // CONNECT
		return
	}
	ack := packets.NewControlPacket(packets.Connack).(*packets.ConnackPacket)
	ack.SessionPresent = true
	if ack.Write(conn) != nil {
		return
	}
	for {
		cp, err := packets.ReadPacket(conn)
		if err != nil {
			return
		}
		p, ok := cp.(*packets.PublishPacket)
		if !ok {
			continue
		}
		got <- p
		switch a {
		case closeIt:
			_ = conn.Close()
		case pubackIt:
			pa := packets.NewControlPacket(packets.Puback).(*packets.PubackPacket)
			pa.MessageID = p.MessageID
			_ = pa.Write(conn)
		case holdIt:
		}
		return
	}
}

func connect(t *testing.T, url string, store *Store) mqtt.Client {
	t.Helper()
	c := mqtt.NewClient(mqtt.NewClientOptions().AddBroker(url).SetClientID("mqttack").
		SetCleanSession(false).SetStore(store).SetAutoReconnect(true).
		SetMaxReconnectInterval(100 * time.Millisecond).SetConnectTimeout(5 * time.Second))
	if tok := c.Connect(); !tok.WaitTimeout(10*time.Second) || tok.Error() != nil {
		t.Fatalf("connect: %v", tok.Error())
	}
	t.Cleanup(func() { c.Disconnect(0) })
	return c
}

// THE PREMISE AND THE COUNTER. A publish the broker closes the connection on, rather than
// PUBACKs, is re-sent on reconnect, and paho completes the publish's token then, with no error,
// although no PUBACK was ever sent. The store does not count it until the PUBACK for the re-send
// arrives.
//
// GUARD: kills a Store that counts a completed token (or any Del other than the PUBACK's) as a
// PUBACK. If paho stops completing the old token on a resume, the premise assertion fails: the
// reason this package exists has changed, and so may what the tests built on it can claim.
func TestATokenCompletedByAResumeIsNotAPuback(t *testing.T) {
	// Connection 1 closes on the publish; connection 2 closes on its re-send; connection 3
	// PUBACKs the second re-send.
	url, got := fakeBroker(t, closeIt, closeIt, pubackIt)
	store := NewStore()
	c := connect(t, url, store)

	tok := c.Publish("t", 1, false, "m")
	if !tok.WaitTimeout(10 * time.Second) {
		t.Fatal("the token was never completed")
	}
	mid := tok.(*mqtt.PublishToken).MessageID()
	first := <-got
	second := <-got
	if first.MessageID != mid || second.MessageID != mid || !second.Dup {
		t.Fatalf("premise: the re-send is the same publish, marked DUP (first %d, second %d dup %v, token %d)",
			first.MessageID, second.MessageID, second.Dup, mid)
	}
	if tok.Error() != nil {
		t.Fatalf("premise: paho completes a resumed publish's token with no error; got %v", tok.Error())
	}
	// Connection 2 may not have closed yet when the token completed (it completes on the
	// resume, before the re-send is answered): the third PUBLISH is the one that is PUBACKed.
	third := <-got
	if third.MessageID != mid {
		t.Fatalf("premise: the second re-send carries id %d, got %d", mid, third.MessageID)
	}
	deadline := time.Now().Add(10 * time.Second)
	for !store.PubAcked(mid) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !store.PubAcked(mid) {
		t.Fatal("the PUBACK the broker did send was not recorded")
	}
	if n := store.Outstanding(); n != 0 {
		t.Fatalf("%d publishes outstanding after the PUBACK", n)
	}
}

// Before any PUBACK, a token completed by a resume is outstanding and not PUBACKed.
func TestAResumedPublishIsOutstandingUntilItsPuback(t *testing.T) {
	// Connection 1 closes on the publish; connection 2 never answers the re-send.
	url, got := fakeBroker(t, closeIt, holdIt)
	store := NewStore()
	c := connect(t, url, store)

	tok := c.Publish("t", 1, false, "m")
	<-got
	if !tok.WaitTimeout(10*time.Second) || tok.Error() != nil {
		t.Fatalf("premise: the token completes on the resume, with no error (%v)", tok.Error())
	}
	mid := tok.(*mqtt.PublishToken).MessageID()
	if store.PubAcked(mid) {
		t.Fatal("a publish no broker PUBACKed was counted as PUBACKed")
	}
	if n := store.Outstanding(); n != 1 {
		t.Fatalf("outstanding = %d, want the 1 unanswered re-send", n)
	}
}
