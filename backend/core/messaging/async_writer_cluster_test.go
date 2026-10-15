// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package messaging

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/streams"
	dctest "github.com/devicechain-io/dc-microservice/test"
	"github.com/nats-io/nats.go"
)

// The AsyncWriter across a stream leader election, on a real three-node replicated cluster.
// The cluster fixture holds a refusing route address, which is implemented only on unix, so
// this runs in CI and not on a Windows workstation (where the build tag keeps it from failing
// on the fixture rather than on the writer).

// storedIDs is the set of Nats-Msg-Id values the resolved stream holds, and how many messages.
func storedIDs(t *testing.T, nmgr *NatsManager) (map[string]int, int) {
	t.Helper()
	stream := StreamName(nmgr.Microservice.InstanceId, streams.ResolvedEvents)
	info, err := nmgr.js.StreamInfo(stream)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]int{}
	for seq := info.State.FirstSeq; seq <= info.State.LastSeq; seq++ {
		raw, err := nmgr.js.GetMsg(stream, seq)
		if err != nil {
			t.Fatalf("stream sequence %d: %v", seq, err)
		}
		ids[raw.Header.Get(nats.MsgIdHdr)]++
	}
	return ids, int(info.State.Msgs)
}

func streamLeader(t *testing.T, nmgr *NatsManager) string {
	t.Helper()
	info, err := nmgr.js.StreamInfo(StreamName(nmgr.Microservice.InstanceId, streams.ResolvedEvents))
	if err != nil {
		t.Fatal(err)
	}
	if info.Cluster == nil {
		t.Fatal("the resolved stream reports no cluster: it is not replicated, so there is no leader to move")
	}
	return info.Cluster.Leader
}

// 🔑 A LEADER ELECTION LOSES NOTHING THE WRITER CALLED STORED, AND STORES NOTHING TWICE.
// Four goroutines share one writer while the stream's leader is stepped down under them.
// Every call whose done said nil must be in the stream afterwards; the calls that failed are
// resubmitted under the same DedupID, from the test's own goroutine (done may not submit),
// until all are stored; and the stream must then hold every id exactly once. The leader is
// checked to have actually moved, or the test proves nothing.
//
// GUARD: kills a done that reports nil before the PubAck (an acked id missing), a writer that
// drops the DedupID (an id stored twice after the retries), and a writer that retries on its
// own behind later messages (the same).
func TestAsyncWriterSurvivesALeaderStepDownWithoutLossOrDuplicates(t *testing.T) {
	const goroutines, perG = 4, 150
	servers := dctest.StartJetStreamCluster(t, 3)
	nmgr := clusterManagerOn(t, servers, uniqueArea("async-stepdown"))
	w, err := nmgr.NewAsyncWriter(streams.ResolvedEvents, 64, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(w.Close)
	waitUntilPublishable(t, nmgr)
	_, base := storedIDs(t, nmgr)
	before := streamLeader(t, nmgr)

	var mu sync.Mutex
	acked := map[string]bool{}
	var failed []string
	record := func(id string) func(error) {
		return func(err error) {
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				acked[id] = true
			} else {
				failed = append(failed, id)
			}
		}
	}
	id := func(g, i int) string { return fmt.Sprintf("resolved:acme:%d-%d:0", g, i) }

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				w.Submit(orderedCtx(), []Message{{Value: []byte("e"), DedupID: id(g, i)}}, record(id(g, i)))
				time.Sleep(2 * time.Millisecond) // spread the run across the election
			}
		}(g)
	}
	// The step-down is made from the test goroutine, while the submitters run.
	time.Sleep(150 * time.Millisecond)
	subj := "$JS.API.STREAM.LEADER.STEPDOWN." + StreamName(nmgr.Microservice.InstanceId, streams.ResolvedEvents)
	retryWhileGroupSettles(t, "step the stream leader down", func() error {
		msg, err := nmgr.nc.Request(subj, nil, 5*time.Second)
		if err != nil {
			return err
		}
		var resp struct {
			Success bool `json:"success"`
		}
		if err := json.Unmarshal(msg.Data, &resp); err != nil || !resp.Success {
			return fmt.Errorf("the stepdown was refused: %s", msg.Data)
		}
		return nil
	})

	wg.Wait()
	settle := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if err := w.Settle(ctx); err != nil {
			t.Fatalf("settle: %v", err)
		}
	}
	settle()

	// Every id the writer reported stored is stored, before any retry can cover for it.
	ids, _ := storedIDs(t, nmgr)
	mu.Lock()
	for a := range acked {
		if ids[a] == 0 {
			mu.Unlock()
			t.Fatalf("id %s was reported stored and is not in the stream", a)
		}
	}
	firstFailures := len(failed)
	mu.Unlock()
	t.Logf("%d of %d calls failed across the election and are retried", firstFailures, goroutines*perG)

	for round := 0; round < 20; round++ {
		mu.Lock()
		retry := failed
		failed = nil
		mu.Unlock()
		if len(retry) == 0 {
			break
		}
		for _, r := range retry {
			w.Submit(orderedCtx(), []Message{{Value: []byte("e"), DedupID: r}}, record(r))
		}
		settle()
		time.Sleep(250 * time.Millisecond)
	}
	mu.Lock()
	left := len(failed)
	mu.Unlock()
	if left != 0 {
		t.Fatalf("%d calls still failed after 20 retry rounds", left)
	}

	if after := streamLeader(t, nmgr); after == before {
		t.Fatalf("the stream leader is still %q: the step-down never happened, so this run tested nothing", before)
	}
	ids, stored := storedIDs(t, nmgr)
	if want := goroutines*perG + base; stored != want {
		t.Fatalf("the stream holds %d messages, want %d: one per id", stored, want)
	}
	for g := 0; g < goroutines; g++ {
		for i := 0; i < perG; i++ {
			if n := ids[id(g, i)]; n != 1 {
				t.Fatalf("id %s is stored %d times, want exactly once", id(g, i), n)
			}
		}
	}
}
