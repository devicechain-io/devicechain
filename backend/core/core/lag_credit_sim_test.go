// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"math"
	"math/rand"
	"sort"
	"time"
)

// The simulation behind the lag-credit property tests. It knows the TRUE send time of every message
// and is the one thing the limiter under test never sees: the limiter is given broker stamps, pod
// time and per-source lag, exactly what a gateway would hold, and the oracle (an offline token bucket
// over the true send sequence) judges the outcome.

var simEpoch = time.Unix(1_800_000_000, 0)

// tokenOracle is the offline token bucket that defines "conformant to (rate, burst)": it starts full
// and a send is conformant when it finds a token.
type tokenOracle struct {
	rate, burst, tokens float64
	last                time.Time
	started             bool
}

func newTokenOracle(rate float64, burst int) *tokenOracle {
	return &tokenOracle{rate: rate, burst: float64(burst), tokens: float64(burst)}
}

// take reports whether a send at t conforms, charging it when it does.
func (o *tokenOracle) take(t time.Time) bool {
	if !o.started {
		o.started, o.last = true, t
	}
	if t.After(o.last) {
		o.tokens = math.Min(o.burst, o.tokens+o.rate*t.Sub(o.last).Seconds())
		o.last = t
	}
	if o.tokens < 1 {
		return false
	}
	o.tokens--
	return true
}

// simMsg is one message of the simulation.
type simMsg struct {
	send  time.Time
	src   int // reader source, or -1 for an unstamped (external MQTT) message
	read  time.Time
	stamp time.Time
	seq   int
}

type simStall struct {
	start time.Time
	dur   time.Duration
}

type simCfg struct {
	rate     float64
	burst    int
	sources  int
	offsets  []time.Duration // the broker's clock offset per source (stamp = send + offset)
	stalls   [][]simStall    // per source
	base     []time.Duration // the read latency per source
	maxStall time.Duration
}

// genSends draws raw send times for horizon seconds in a mix of steady, bursting and silent
// stretches, at up to several times the ceiling.
func genSends(rng *rand.Rand, rate float64, burst int, horizon time.Duration, overload float64) []time.Time {
	var out []time.Time
	t := simEpoch
	end := simEpoch.Add(horizon)
	for t.Before(end) {
		span := time.Duration(50+rng.Intn(1500)) * time.Millisecond
		mode := rng.Intn(4)
		var lambda float64 // sends per second in this stretch
		switch mode {
		case 0:
			lambda = rate * (0.3 + rng.Float64()*0.7)
		case 1:
			lambda = rate * (1 + rng.Float64()*overload)
		case 2:
			lambda = 0
		case 3:
			// an instantaneous burst of up to burst messages
			n := 1 + rng.Intn(max(burst, 1))
			for i := 0; i < n; i++ {
				out = append(out, t.Add(time.Duration(rng.Intn(1000))*time.Microsecond))
			}
			lambda = rate
		}
		if lambda > 0 {
			step := time.Duration(float64(time.Second) / lambda)
			for at := t; at.Before(t.Add(span)) && at.Before(end); at = at.Add(step) {
				out = append(out, at)
			}
		}
		t = t.Add(span)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Before(out[j]) })
	return out
}

// conform keeps the sends the offline oracle accepts: a send sequence that conforms to (rate, burst).
func conform(sends []time.Time, rate float64, burst int) []time.Time {
	o := newTokenOracle(rate, burst)
	var out []time.Time
	for _, s := range sends {
		if o.take(s) {
			out = append(out, s)
		}
	}
	return out
}

// randomCfg draws a ceiling, sources, skews and stalls.
func randomCfg(rng *rand.Rand, horizon time.Duration) simCfg {
	rate := float64(20 + rng.Intn(1500))
	burst := 1 + rng.Intn(3000)
	if rng.Intn(5) == 0 {
		burst = int(rate * (0.2 + rng.Float64()*8)) // cover from 0.2 s to past the 5 s cap
		burst = max(burst, 1)
	}
	cfg := simCfg{rate: rate, burst: burst, sources: 1 + rng.Intn(3)}
	cover := LagCreditMax(TenantCeiling{RatePerSecond: rate, Burst: burst})
	cfg.base = make([]time.Duration, cfg.sources)
	cfg.offsets = make([]time.Duration, cfg.sources)
	cfg.stalls = make([][]simStall, cfg.sources)
	for k := 0; k < cfg.sources; k++ {
		cfg.base[k] = time.Duration(rng.Intn(20)) * time.Millisecond
		cfg.base[k] = min(cfg.base[k], cover/4)
		cfg.offsets[k] = time.Duration(rng.Intn(600)-300) * time.Millisecond
		t := simEpoch
		for t.Before(simEpoch.Add(horizon)) && rng.Intn(6) != 0 {
			t = t.Add(time.Duration(rng.Intn(2000)) * time.Millisecond)
			dur := time.Duration(rng.Float64() * float64(cover-cfg.base[k]))
			if rng.Intn(3) == 0 {
				dur = cover - cfg.base[k] // the worst stall the credit must cover
			}
			cfg.stalls[k] = append(cfg.stalls[k], simStall{start: t, dur: dur})
			t = t.Add(dur)
		}
	}
	cfg.maxStall = cover
	return cfg
}

// readTime is when source k's reader reads a message sent at s: after its base latency, or when the
// stall it falls inside ends. Monotone in s, so a reader stays FIFO.
func (c simCfg) readTime(k int, s time.Time) time.Time {
	r := s.Add(c.base[k])
	for _, st := range c.stalls[k] {
		if !r.Before(st.start) && r.Before(st.start.Add(st.dur)) {
			r = st.start.Add(st.dur)
		}
	}
	return r
}

// buildMsgs assigns each send a source (or unstamped when live is true for some) and a read time.
func buildMsgs(rng *rand.Rand, cfg simCfg, sends []time.Time, liveShare float64) []simMsg {
	msgs := make([]simMsg, 0, len(sends))
	cur := rng.Intn(cfg.sources)
	for i, s := range sends {
		if rng.Intn(4) == 0 {
			cur = rng.Intn(cfg.sources) // blocks on one source, then a switch
		}
		m := simMsg{send: s, src: cur, seq: i}
		if liveShare > 0 && rng.Float64() < liveShare {
			m.src = -1
			m.read = s
		} else {
			m.read = cfg.readTime(cur, s)
			m.stamp = s.Add(cfg.offsets[cur])
		}
		msgs = append(msgs, m)
	}
	return msgs
}

// order returns the processing order: by read time, with sources interleaved arbitrarily at the same
// instant but every source FIFO.
func order(rng *rand.Rand, msgs []simMsg, sources int) []simMsg {
	// per-source queues in send order (read times are monotone in send time within a source)
	queues := make([][]simMsg, sources+1)
	for _, m := range msgs {
		q := m.src
		if q < 0 {
			q = sources
		}
		queues[q] = append(queues[q], m)
	}
	var out []simMsg
	heads := make([]int, len(queues))
	for {
		// the earliest read time among queue heads
		var best time.Time
		have := false
		for q := range queues {
			if heads[q] < len(queues[q]) {
				r := queues[q][heads[q]].read
				if !have || r.Before(best) {
					best, have = r, true
				}
			}
		}
		if !have {
			return out
		}
		var ready []int
		for q := range queues {
			if heads[q] < len(queues[q]) && queues[q][heads[q]].read.Equal(best) {
				ready = append(ready, q)
			}
		}
		q := ready[rng.Intn(len(ready))]
		out = append(out, queues[q][heads[q]])
		heads[q]++
	}
}

// laggingSources is the exact corrected head-of-line lag of every source at now, given what each has
// processed so far: the age of its next unread message, or 0 when it has none appended.
func laggingSources(cfg simCfg, queues [][]simMsg, heads []int, now time.Time) []LagCreditSource {
	srcs := make([]LagCreditSource, cfg.sources)
	for k := 0; k < cfg.sources; k++ {
		srcs[k].Skew = -cfg.offsets[k] // the estimate is exact: stamp − send = offset
		if heads[k] < len(queues[k]) {
			if head := queues[k][heads[k]]; !head.send.After(now) {
				srcs[k].Lag = now.Sub(head.send)
			}
		}
	}
	return srcs
}

// splitQueues returns each source's messages in processing (send) order.
func splitQueues(msgs []simMsg, sources int) [][]simMsg {
	queues := make([][]simMsg, sources)
	for _, m := range msgs {
		if m.src >= 0 {
			queues[m.src] = append(queues[m.src], m)
		}
	}
	return queues
}
