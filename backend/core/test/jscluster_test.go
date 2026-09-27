// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
)

// A cluster that has formed can lose the ability to place a replicated stream when a
// route is registered again, because registering it wipes what the server knew about the
// peer. The readiness wait must not report formed until placement works again, and then
// replicated streams must place with no retry at all.
func TestAFormedClusterPlacesReplicatedStreamsAgainAfterARouteIsRedialled(t *testing.T) {
	severThenAwaitThenPlace(t, formedNow)
}

// The same, with the statistics rule taken out of the per-poll check: the placement
// probe alone has to hold the gate shut until placement works again. The rule and the
// probe each cover for the other, so without this test the probe could be skipped
// entirely and nothing would notice.
func TestThePlacementProbeAloneHoldsTheGateAcrossARedial(t *testing.T) {
	severThenAwaitThenPlace(t, func(views []serverView) error {
		_, err := metaFormed(views)
		return err
	})
}

// severThenAwaitThenPlace cuts one server's routes on a formed cluster, waits with check
// as the per-poll rule, and then requires three R3 streams to place with no retry.
func severThenAwaitThenPlace(t *testing.T, check func([]serverView) error) {
	t.Helper()
	servers, faults := StartJetStreamClusterWithRouteFaults(t, 3)

	// Watch the meta leader's view of which servers it holds statistics for, from the
	// moment the routes are cut until the wait returns: the evidence that the cut reached
	// the wipe, rather than an inference from an error. It is read through viewOf, the
	// same read the wait's statistics rule takes, so a wait fed anything but the leader's
	// real answer shows here as a wipe that was never seen.
	var mu sync.Mutex
	fewest := len(servers)
	wiped := make(chan struct{})
	var wipedOnce sync.Once
	stop := make(chan struct{})
	watched := make(chan struct{})
	go func() {
		defer close(watched)
		for {
			for _, srv := range servers {
				if !srv.JetStreamIsLeader() {
					continue
				}
				// A server that lost the leadership between the two calls answers nil;
				// only a leader's answer counts, and a leader always lists itself.
				if n := len(viewOf(srv).statsPeers); n > 0 {
					mu.Lock()
					fewest = min(fewest, n)
					mu.Unlock()
					if n < len(servers) {
						wipedOnce.Do(func() { close(wiped) })
					}
				}
			}
			select {
			case <-stop:
				return
			case <-time.After(10 * time.Millisecond):
			}
		}
	}()

	if n := faults.sever(1); n == 0 {
		t.Fatal("sever closed no route connections, so no route was registered again and the test proves nothing")
	}
	// Start the wait only once the wipe has been seen. A wait started earlier can find
	// the cluster still formed on the old routes and return before the cut lands, which
	// says nothing about what the wait does over a wipe.
	select {
	case <-wiped:
	case <-time.After(15 * time.Second):
		close(stop)
		<-watched
		t.Fatalf("the meta leader held statistics for all %d servers for 15 s after the cut, so cutting the "+
			"routes never wiped them and the test did not reproduce what it guards against", len(servers))
	}
	err := awaitFormed(servers, clusterFormBudget, check)
	close(stop)
	<-watched
	if err != nil {
		t.Fatal(err)
	}

	nc, err := nats.Connect(servers[0].ClientURL())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer nc.Close()
	js, err := nc.JetStream()
	if err != nil {
		t.Fatalf("jetstream: %v", err)
	}
	// The probe is the wait's own business: by the time it returns the stream is gone.
	if _, err := js.StreamInfo(formedProbeStream); !errors.Is(err, nats.ErrStreamNotFound) {
		t.Fatalf("the probe stream %s is still there after the wait returned (StreamInfo error: %v)", formedProbeStream, err)
	}
	for k := 0; k < 3; k++ {
		name := fmt.Sprintf("AFTER_REDIAL_%d", k)
		if _, err := js.AddStream(&nats.StreamConfig{
			Name:     name,
			Subjects: []string{fmt.Sprintf("_after_redial.%d", k)},
			Storage:  nats.FileStorage,
			Replicas: 3,
		}); err != nil {
			t.Fatalf("placing %s at R3 right after the cluster was reported formed: %v", name, err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	t.Logf("the meta leader held statistics for as few as %d of %d servers after the cut", fewest, len(servers))
}

func TestFormedNowNamesWhatIsMissing(t *testing.T) {
	formed := func() []serverView {
		return []serverView{
			{name: "n1", remotes: 2, metaLeader: "n1",
				replicas: []*natsserver.PeerInfo{
					{Name: "n2", Current: true},
					{Name: "n3", Current: true},
				},
				statsPeers: []string{"n1", "n2", "n3"},
			},
			{name: "n2", remotes: 2, metaLeader: "n1"},
			{name: "n3", remotes: 2, metaLeader: "n1"},
		}
	}
	cases := []struct {
		name   string
		change func(v []serverView)
		want   string // "" means formed
	}{
		{"formed", func([]serverView) {}, ""},
		{"short mesh", func(v []serverView) { v[2].remotes = 1 }, "n3 is routed to 1 of 2 other servers"},
		{"no leader", func(v []serverView) { v[1].metaLeader = "" }, "n2 knows no meta leader"},
		{"split", func(v []serverView) { v[2].metaLeader = "n2" }, `n3 names "n2"`},
		{"leader absent", func(v []serverView) {
			for i := range v {
				v[i].metaLeader = "n9"
			}
		}, `meta leader "n9" is not one of these servers`},
		{"short peers", func(v []serverView) { v[0].replicas = v[0].replicas[:1] }, "meta leader n1 reports 1 peers, want 2"},
		{"not current", func(v []serverView) { v[0].replicas[1].Current = false }, "meta leader n1 reports peer n3 not current"},
		{"offline", func(v []serverView) { v[0].replicas[1].Offline = true }, "meta leader n1 reports peer n3 offline"},
		{"stats missing", func(v []serverView) { v[0].statsPeers = []string{"n1", "n2"} }, "meta leader n1 has no stats for n3"},
		{"leader's own stats missing", func(v []serverView) { v[0].statsPeers = []string{"n2", "n3"} }, "meta leader n1 has no stats for n1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			views := formed()
			c.change(views)
			err := formedNow(views)
			if c.want == "" {
				if err != nil {
					t.Fatalf("formedNow = %v, want nil for a formed cluster", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("formedNow = %v, want an error containing %q", err, c.want)
			}
		})
	}
}
