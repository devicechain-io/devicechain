// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"path"
	"time"

	"github.com/devicechain-io/dc-edge-agent/config"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// PRIORITY-CLASS SHEDDING.
//
// The spool is bounded, and a bounded spool that outlasts its budget must lose something. Its own
// ring buffer loses the OLDEST event whatever it is, which during a long outage means the early
// alarms go before the later chatter. With local.priority rules set, the agent sheds on purpose
// first: whenever the spool passes shedAtFraction of its budget it deletes buffered events,
// lowest class first and oldest first within a class, down to shedToFraction.
//
//	bulk      raw high-rate data that is cheap to lose and was going to be reduced anyway
//	standard  everything that matches no rule
//	safety    never shed by the agent
//
// 🔴 "NEVER" HAS A BOUNDARY, AND IT IS THE BUDGET. The agent never deletes a safety event on
// purpose. But the spool is a ring buffer underneath: if safety events alone fill the budget,
// or arrive faster than a shed pass can free room, the ring evicts the oldest event of any
// class and the loss shows only in dropped_total. shedAtFraction is the headroom that makes
// this unlikely, not impossible; size the spool for the safety traffic of the longest outage.
// Every event shed on purpose is counted by class (shed_total{class}).

const (
	classBulk = iota
	classStandard
	classSafety
	numClasses
)

var classNames = [numClasses]string{config.ClassBulk, config.ClassStandard, config.ClassSafety}

const (
	// shedEvery is how often the shedder looks at the spool.
	shedEvery = 2 * time.Second
	// shedScanLimit bounds the messages one pass reads, so a very deep spool cannot hold the
	// shedder in one pass; the next pass continues.
	shedScanLimit = 50000
	// shedMsgOverhead approximates the per-message storage cost on top of subject and body.
	shedMsgOverhead = 32
)

type classifier struct{ rules []config.PriorityRule }

func newClassifier(rules []config.PriorityRule) *classifier { return &classifier{rules: rules} }

// classOf returns the class of a local device's events: the first matching rule, else standard.
func (c *classifier) classOf(device string) int {
	for _, r := range c.rules {
		if ok, _ := path.Match(r.Match, device); ok {
			switch r.Class {
			case config.ClassBulk:
				return classBulk
			case config.ClassSafety:
				return classSafety
			}
			return classStandard
		}
	}
	return classStandard
}

func (a *Agent) shedLoop(ctx context.Context) {
	if len(a.cfg.Local.Priority.Rules) == 0 {
		return
	}
	t := time.NewTicker(shedEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.shedOnce()
		}
	}
}

// shedOnce brings the spool back under shedToFraction when it is over shedAtFraction, and
// returns the events shed per class.
func (a *Agent) shedOnce() [numClasses]int {
	var shed [numClasses]int
	p := a.cfg.Local.Priority
	limit := a.cfg.Local.SpoolMaxBytes
	info, err := a.js.StreamInfo(captureStream)
	if err != nil {
		return shed
	}
	used := int64(info.State.Bytes)
	if float64(used) < p.ShedAtFraction*float64(limit) {
		return shed
	}
	need := used - int64(p.ShedToFraction*float64(limit))
	var freed int64
	// Lowest class first; safety is not in the list, which is what "never" means here.
	for _, cls := range []int{classBulk, classStandard} {
		scanned := 0
		for seq := info.State.FirstSeq; seq <= info.State.LastSeq && freed < need && scanned < shedScanLimit; seq++ {
			scanned++
			raw, err := a.js.GetMsg(captureStream, seq)
			if err != nil {
				continue // already gone (acked, shed, or evicted)
			}
			if a.classOfSubject(raw.Subject) != cls {
				continue
			}
			if err := a.js.DeleteMsg(captureStream, seq); err != nil {
				continue
			}
			freed += int64(len(raw.Data) + len(raw.Subject) + shedMsgOverhead)
			shed[cls]++
		}
		if freed >= need {
			break
		}
	}
	a.recordShed(shed, used, limit)
	return shed
}

func (a *Agent) classOfSubject(subject string) int {
	_, child, ok := messaging.ParseDeviceEventsSubject(subject)
	if !ok {
		return classStandard
	}
	return a.classes.classOf(child)
}

func (a *Agent) recordShed(shed [numClasses]int, used, limit int64) {
	total := 0
	for cls, n := range shed {
		if n == 0 {
			continue
		}
		total += n
		a.shedByClass[cls].Add(int64(n))
		if a.metrics != nil {
			a.metrics.shed.WithLabelValues(classNames[cls]).Add(float64(n))
		}
	}
	if total == 0 {
		return
	}
	// A shed event left the stream without an ack, and the drop metric reads removals it
	// cannot account for as ring evictions: count these as removed so it does not.
	a.ackedCount.Add(uint64(total))
	a.log.Warn("spool over its shed threshold: shed buffered events by priority class (safety is never shed)",
		"shed_bulk", shed[classBulk], "shed_standard", shed[classStandard],
		"spool_used_bytes", used, "spool_max_bytes", limit)
}
