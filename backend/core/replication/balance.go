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

// Spread is how much each server leads: a COUNT of groups when the plan is unweighted, the sum of
// the groups' weights when PlanOptions.Weight is set.
type Spread map[string]int

// Max returns the largest and smallest value over the servers.
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

// Balanced reports whether no server leads more than one above any other.
// A spread of two over three groups (2,1,0) is not, because two groups can still be
// moved to even it out; one of 1,1,0 is, because nothing can improve it.
func (s Spread) Balanced() bool {
	hi, lo := s.Max()
	return hi-lo <= 1
}

// RebalancePlan is what to ask the broker to do, and what that would leave.
type RebalancePlan struct {
	Moves []Move
	// StreamsBefore and ConsumersBefore are the groups led per server before the plan, as counts.
	StreamsBefore, ConsumersBefore Spread
	// StreamsAfter and ConsumersAfter are the groups led per server if every move lands. With
	// weights they include the fixed groups, so they can differ from the count of movable ones.
	StreamsAfter, ConsumersAfter Spread
	// LoadBefore and LoadAfter are what each server pays for the streams and consumers it leads: the
	// sum of their weights, the fixed groups included. Without a Weight it is the number of groups.
	LoadBefore, LoadAfter Spread
	// Servers is every server seen, sorted.
	Servers []string
}

// group is one RAFT group with a leader and the servers that could take over.
type group struct {
	stream, consumer string
	leader           string
	// candidates are the replicas that are current and online, sorted by name.
	candidates []string
	// weight is what leading the group costs a server, at least 1.
	weight int
	// fixed groups carry load but are never moved (no candidates).
	fixed bool
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
	// Weight is what a stream costs the server that leads it (a consumer weighs what its stream
	// does), so the plan balances TRAFFIC rather than the number of groups. Nil means every movable
	// group weighs 1 and groups that are not movable are ignored: the count-based plan.
	//
	// When set, a stream with a positive weight that is NOT movable is still counted, as a FIXED
	// anchor: it is never moved, but the servers' loads include it, so a movable stream is placed
	// away from the hot stream it may not move. A weight of zero or less leaves the stream out of
	// the arithmetic. Weights only add anchors; the set of groups that can move is Movable's alone.
	Weight func(stream string) int
}

// PlanRebalance works out, from one snapshot, the leader step-downs that even out what each server
// pays for the streams and consumers it leads: their number, or with PlanOptions.Weight their
// traffic.
//
// 🔴 WHY IT EXISTS. After a rolling restart the server restarted last leads nothing: each
// server that went down handed its groups to the others, and a RAFT group never gives
// leadership back on its own. Two servers then carry every group's replication fan-out.
//
// It only ever moves a group to a replica that is current and online, and only when that
// strictly reduces the gap (the source leads more than the group's weight above the destination;
// with every weight 1, at least two more), so it
// terminates and never trades one imbalance for another. A group with no such replica (a
// single-replica group, or one whose peers lag) is counted but left where it is. Groups on streams
// the options do not name are neither counted nor moved.
func PlanRebalance(snap Snapshot, opts PlanOptions) RebalancePlan {
	maxMoves := opts.MaxMoves
	movable := func(stream string) bool { return opts.Movable != nil && opts.Movable(stream) }
	plan := RebalancePlan{}
	servers := map[string]bool{}
	var streams, consumers []*group
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
	// weigh is the group's weight, and whether it is in the arithmetic at all.
	weigh := func(stream string) (w int, fixed, in bool) {
		mv := movable(stream)
		if opts.Weight == nil {
			return 1, false, mv
		}
		w = opts.Weight(stream)
		if mv {
			return max(w, 1), false, true
		}
		return w, true, w > 0
	}
	for _, o := range snap.Objects {
		w, fixed, in := weigh(o.Name)
		if !in {
			continue
		}
		c := collect(o.Leader, o.Peers)
		if fixed {
			c = nil
		}
		if o.Leader != "" {
			streams = append(streams, &group{stream: o.Name, leader: o.Leader, candidates: c, weight: w, fixed: fixed})
		}
	}
	for _, c := range snap.Consumers {
		w, fixed, in := weigh(c.Stream)
		if !in {
			continue
		}
		cands := collect(c.Leader, c.Peers)
		if fixed {
			cands = nil
		}
		if c.Leader != "" && c.Durable {
			consumers = append(consumers, &group{stream: c.Stream, consumer: c.Name, leader: c.Leader, candidates: cands, weight: w, fixed: fixed})
		}
	}
	for s := range servers {
		plan.Servers = append(plan.Servers, s)
	}
	sort.Strings(plan.Servers)

	// spread is the load each server carries: the sum of weights (a count when unweighted).
	spread := func(gs ...[]*group) Spread {
		sp := Spread{}
		for _, s := range plan.Servers {
			sp[s] = 0
		}
		for _, one := range gs {
			for _, g := range one {
				sp[g.leader] += g.weight
			}
		}
		return sp
	}
	// counts is how many groups each server leads, whatever they weigh.
	counts := func(gs []*group) Spread {
		sp := Spread{}
		for _, s := range plan.Servers {
			sp[s] = 0
		}
		for _, g := range gs {
			sp[g.leader]++
		}
		return sp
	}
	plan.LoadBefore = spread(streams, consumers)
	plan.StreamsBefore, plan.ConsumersBefore = counts(streams), counts(consumers)

	budget := maxMoves
	// balance moves groups until no move shrinks the gap of the pair it touches. A group of weight
	// w goes from a to b only when load[a]-load[b] > w, so each move strictly lowers the sum of
	// squared loads: the loop terminates and never trades one imbalance for another. With every
	// weight 1 that is the count rule (a gap of two or more). Of the moves that qualify the one
	// that lowers the sum of squares most goes first, which sends a group to the LIGHTEST replica
	// and moves the hot groups ahead of the cold, so a plan cut short by MaxMoves has done the
	// useful part.
	balance := func(gs []*group, sp Spread) {
		sort.Slice(gs, func(i, j int) bool {
			if gs[i].stream != gs[j].stream {
				return gs[i].stream < gs[j].stream
			}
			return gs[i].consumer < gs[j].consumer
		})
		for maxMoves <= 0 || budget > 0 {
			bestScore, bi, bto := 0, -1, ""
			for i, g := range gs {
				for _, c := range g.candidates {
					gap := sp[g.leader] - sp[c]
					if gap <= g.weight {
						continue
					}
					score := 2 * g.weight * (gap - g.weight)
					if score > bestScore || score == bestScore && sp[g.leader] > sp[gs[bi].leader] {
						bestScore, bi, bto = score, i, c
					}
				}
			}
			if bi < 0 {
				break
			}
			g := gs[bi]
			plan.Moves = append(plan.Moves, Move{Stream: g.stream, Consumer: g.consumer, From: g.leader, To: bto})
			sp[g.leader] -= g.weight
			sp[bto] += g.weight
			g.leader = bto
			g.candidates = nil // moved once; leave the rest of the plan to the next snapshot
			budget--
		}
	}
	if opts.Weight == nil {
		// Unweighted: streams and consumers are two separate pools, each evened out on its own.
		balance(streams, spread(streams))
		balance(consumers, spread(consumers))
	} else {
		// Weighted: a server pays for the streams it leads AND the consumers it leads, so they are
		// one pool. Balanced apart, a stream could be sent to the server that already leads the
		// consumers of equally hot streams and leave the total worse.
		all := append(append([]*group{}, streams...), consumers...)
		balance(all, spread(all))
	}
	plan.LoadAfter = spread(streams, consumers)
	plan.StreamsAfter, plan.ConsumersAfter = counts(streams), counts(consumers)
	return plan
}
