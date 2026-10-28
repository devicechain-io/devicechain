// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package replication_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/replication"
	dctest "github.com/devicechain-io/dc-microservice/test"
	nats "github.com/nats-io/nats.go"
)

// leaderSpread reads, from the broker, how many streams each server leads.
func leaderSpread(t *testing.T, js nats.JetStreamContext, names []string) map[string]int {
	t.Helper()
	out := map[string]int{}
	for _, n := range names {
		si, err := js.StreamInfo(n)
		if err != nil || si.Cluster == nil {
			// Mid-election the broker may not answer; that is "no leader yet".
			out[""]++
			continue
		}
		out[si.Cluster.Leader]++
	}
	return out
}

// The case the GKE round measured, reproduced on a real three-server cluster: every stream
// is led by two of the servers (what a rolling restart leaves), the plan moves leadership
// onto the third, and the broker agrees afterwards.
func TestRebalanceEvensOutLeadershipOnARealCluster(t *testing.T) {
	servers := dctest.StartJetStreamCluster(t, 3)
	nc, err := nats.Connect(servers[0].ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	js, err := nc.JetStream()
	if err != nil {
		t.Fatal(err)
	}

	var names []string
	for i := 0; i < 9; i++ {
		name := fmt.Sprintf("REBAL_%d", i)
		if _, err := js.AddStream(&nats.StreamConfig{Name: name, Subjects: []string{fmt.Sprintf("rebal.%d", i)},
			Storage: nats.FileStorage, Replicas: 3}); err != nil {
			t.Fatalf("add %s: %v", name, err)
		}
		if _, err := js.AddConsumer(name, &nats.ConsumerConfig{Durable: "reader", AckPolicy: nats.AckExplicitPolicy}); err != nil {
			t.Fatalf("add consumer: %v", err)
		}
		names = append(names, name)
	}

	// Take leadership off one server, as the last restart does.
	excluded := servers[2].Name()
	moved := 0
	for _, n := range names {
		si, err := js.StreamInfo(n)
		if err != nil || si.Cluster.Leader != excluded {
			continue
		}
		stepDown(t, nc, js, n, servers[moved%2].Name())
		moved++
	}
	waitFor(t, func() bool {
		sp := leaderSpread(t, js, names)
		return sp[""] == 0 && sp[excluded] == 0
	})
	before := leaderSpread(t, js, names)

	exp := replication.Expectation{Prefixes: []string{"REBAL_"}}
	snap, err := replication.Collect(js, exp)
	if err != nil {
		t.Fatal(err)
	}
	plan := replication.PlanRebalance(snap, replication.PlanOptions{Movable: func(string) bool { return true }})
	if len(plan.Moves) == 0 {
		t.Fatalf("an unbalanced cluster %v produced no plan", before)
	}
	for _, r := range replication.ApplyMoves(context.Background(), nc, js, plan.Moves, replication.ApplyOptions{PerMove: 10 * time.Second}) {
		if r.Err != nil {
			t.Errorf("move %+v: %v", r.Move, r.Err)
		}
	}
	waitFor(t, func() bool {
		sp := leaderSpread(t, js, names)
		return sp[""] == 0 && sp[excluded] == 3 && len(sp) == 3
	})
	after := leaderSpread(t, js, names)
	t.Logf("leaders per server: before %v, after %v", before, after)
	for s, n := range after {
		if n != 3 {
			t.Errorf("%s leads %d streams, want 3 of 9", s, n)
		}
	}
	// A move planned from an old snapshot (the leader is not who the plan saw) is skipped, not sent.
	stale := replication.Move{Stream: names[0], From: "not-the-leader", To: servers[0].Name()}
	if res := replication.ApplyMoves(context.Background(), nc, js, []replication.Move{stale}, replication.ApplyOptions{PerMove: 5 * time.Second}); !res[0].Skipped || res[0].Err != nil {
		t.Errorf("a stale move was not skipped: %+v", res[0])
	}
	snap, err = replication.Collect(js, exp)
	if err != nil {
		t.Fatal(err)
	}
	if again := replication.PlanRebalance(snap, replication.PlanOptions{Movable: func(string) bool { return true }}); len(again.StreamsBefore) == 0 || !again.StreamsBefore.Balanced() {
		t.Errorf("plan on the result is not balanced: %v", again.StreamsBefore)
	}
}

func stepDown(t *testing.T, nc *nats.Conn, js nats.JetStreamContext, stream, to string) {
	t.Helper()
	res := replication.ApplyMoves(context.Background(), nc, js, []replication.Move{{Stream: stream, From: leaderNow(t, js, stream), To: to}}, replication.ApplyOptions{PerMove: 10 * time.Second})
	if res[0].Err != nil {
		t.Fatalf("step down %s: %v", stream, res[0].Err)
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for leadership to settle")
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func leaderNow(t *testing.T, js nats.JetStreamContext, stream string) string {
	t.Helper()
	si, err := js.StreamInfo(stream)
	if err != nil || si.Cluster == nil {
		t.Fatalf("stream info %s: %v", stream, err)
	}
	return si.Cluster.Leader
}
