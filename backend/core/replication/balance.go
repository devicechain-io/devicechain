// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package replication

import "sort"

// Move asks one RAFT group's leader to hand leadership to a named server.
type Move struct {
	// Stream is the stream, or the stream a consumer belongs to.
	Stream string
	// Consumer is empty for a stream move.
	Consumer string
	// From is the server that leads now, To the one that should.
	From, To string
}

// IsConsumer reports whether the move is a consumer's.
func (m Move) IsConsumer() bool { return m.Consumer != "" }

// Spread is how many RAFT groups each server leads.
type Spread map[string]int

// Max returns the largest and smallest count over the servers.
func (s Spread) Max() (hi, lo int) {
	first := true
	for _, n := range s {
		if first || n > hi {
			hi = n
		}
		if first || n < lo {
			lo = n
		}
		first = false
	}
	return hi, lo
}

// Balanced reports whether no server leads more than one group above any other.
// A spread of two over three groups (2,1,0) is not, because two groups can still be
// moved to even it out; one of 1,1,0 is, because nothing can improve it.
func (s Spread) Balanced() bool {
	hi, lo := s.Max()
	return hi-lo <= 1
}

// RebalancePlan is what to ask the broker to do, and what that would leave.
type RebalancePlan struct {
	Moves []Move
	// Streams and Consumers are the leaders per server before the plan.
	StreamsBefore, ConsumersBefore Spread
	// StreamsAfter and ConsumersAfter are the leaders per server if every move lands.
	StreamsAfter, ConsumersAfter Spread
	// Servers is every server seen, sorted.
	Servers []string
}

// group is one RAFT group with a leader and the servers that could take over.
type group struct {
	stream, consumer string
	leader           string
	// candidates are the replicas that are current and online, sorted by name.
	candidates []string
}

// PlanOptions bound a plan.
type PlanOptions struct {
	// Movable says which streams may have their leaders moved, by stream name (a consumer is judged
	// by its stream). It is REQUIRED: nil means nothing is movable, never everything, because the
	// caller knows what is unsafe to move (device data already acknowledged to the device) and this
	// package does not.
	Movable func(stream string) bool
	// MaxMoves bounds the plan; zero or less means no bound beyond the rules below.
	MaxMoves int
}

// PlanRebalance works out, from one snapshot, the leader step-downs that even out how many
// streams and how many consumers each server leads.
//
// 🔴 WHY IT EXISTS. After a rolling restart the server restarted last leads nothing: each
// server that went down handed its groups to the others, and a RAFT group never gives
// leadership back on its own. Two servers then carry every group's replication fan-out.
//
// It only ever moves a group to a replica that is current and online, and only when that
// strictly reduces the gap (the source leads at least two more than the destination), so it
// terminates and never trades one imbalance for another. A group with no such replica (a
// single-replica group, or one whose peers lag) is counted but left where it is. Groups on streams
// the options do not name are neither counted nor moved.
func PlanRebalance(snap Snapshot, opts PlanOptions) RebalancePlan {
	maxMoves := opts.MaxMoves
	movable := func(stream string) bool { return opts.Movable != nil && opts.Movable(stream) }
	plan := RebalancePlan{}
	servers := map[string]bool{}
	var streams, consumers []group
	collect := func(leader string, peers []Peer) []string {
		if leader != "" {
			servers[leader] = true
		}
		var cands []string
		for _, p := range peers {
			servers[p.Name] = true
			if p.Current && !p.Offline && p.Name != leader {
				cands = append(cands, p.Name)
			}
		}
		sort.Strings(cands)
		return cands
	}
	for _, o := range snap.Objects {
		if !movable(o.Name) {
			continue
		}
		c := collect(o.Leader, o.Peers)
		if o.Leader != "" {
			streams = append(streams, group{stream: o.Name, leader: o.Leader, candidates: c})
		}
	}
	for _, c := range snap.Consumers {
		if !movable(c.Stream) {
			continue
		}
		cands := collect(c.Leader, c.Peers)
		if c.Leader != "" && c.Durable {
			consumers = append(consumers, group{stream: c.Stream, consumer: c.Name, leader: c.Leader, candidates: cands})
		}
	}
	for s := range servers {
		plan.Servers = append(plan.Servers, s)
	}
	sort.Strings(plan.Servers)

	spread := func(gs []group) Spread {
		sp := Spread{}
		for _, s := range plan.Servers {
			sp[s] = 0
		}
		for _, g := range gs {
			sp[g.leader]++
		}
		return sp
	}
	plan.StreamsBefore, plan.ConsumersBefore = spread(streams), spread(consumers)

	budget := maxMoves
	balance := func(gs []group) Spread {
		sp := spread(gs)
		sort.Slice(gs, func(i, j int) bool {
			if gs[i].stream != gs[j].stream {
				return gs[i].stream < gs[j].stream
			}
			return gs[i].consumer < gs[j].consumer
		})
		for maxMoves <= 0 || budget > 0 {
			bestGain, bi, bto := 0, -1, ""
			for i, g := range gs {
				for _, c := range g.candidates {
					if gain := sp[g.leader] - sp[c]; gain >= 2 && (gain > bestGain ||
						gain == bestGain && sp[g.leader] > sp[gs[bi].leader]) {
						bestGain, bi, bto = gain, i, c
					}
				}
			}
			if bi < 0 {
				break
			}
			g := &gs[bi]
			plan.Moves = append(plan.Moves, Move{Stream: g.stream, Consumer: g.consumer, From: g.leader, To: bto})
			sp[g.leader]--
			sp[bto]++
			g.leader = bto
			g.candidates = nil // moved once; leave the rest of the plan to the next snapshot
			budget--
		}
		return sp
	}
	plan.StreamsAfter = balance(streams)
	plan.ConsumersAfter = balance(consumers)
	return plan
}
