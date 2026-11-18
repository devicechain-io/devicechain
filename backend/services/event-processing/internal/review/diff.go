// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package review

import (
	"encoding/json"
	"sort"
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/evaluate"
	"github.com/devicechain-io/dc-event-processing/internal/rules"
)

type groupKey struct{ device, subject, discriminator string }
type element struct {
	key     groupKey
	at      time.Time
	content Content
	reading ReadingRef
}

func elements(edges []evaluate.Edge, intents []evaluate.Intent, s side) []element {
	var out []element
	type causeKey struct {
		device string
		at     time.Time
		raise  bool
	}
	causes := map[causeKey]ReadingRef{}
	for _, e := range edges {
		ref := ReadingRef{Device: e.Series, At: e.At, Value: e.Value, HasValue: e.HasValue}
		if s.def.Type == rules.TypeAggregate {
			ref.Span = s.def.Window.D()
		}
		causes[causeKey{e.Series, e.At.UTC(), e.Raise}] = ref
	}
	ref := func(dev string, at time.Time, raise bool) ReadingRef {
		if r, ok := causes[causeKey{dev, at.UTC(), raise}]; ok {
			return r
		}
		return ReadingRef{Device: dev, At: at}
	}

	for _, e := range edges {
		subject := "EDGE_RESOLVED"
		if e.Raise {
			subject = "EDGE_RAISED"
		}
		out = append(out, element{groupKey{e.Series, subject, e.RuleToken}, e.At, Content{Severity: e.Severity}, ref(e.Series, e.At, e.Raise)})
	}
	for _, in := range intents {
		c := Content{}
		disc := ""
		switch in.Kind {
		case evaluate.KindRaiseAlarm, evaluate.KindClearAlarm:
			disc = in.AlarmKey
			c.Severity = in.Severity
		case evaluate.KindSendCommand:
			disc = in.Command
			c.Payload = canonicalJSON(in.Payload)
			for _, a := range s.def.Actions {
				if a.SendCommand == nil || a.SendCommand.Command != in.Command || canonicalJSON(a.SendCommand.Payload) != c.Payload || a.SendCommand.ExpiresAfter == "" {
					continue
				}
				expiry, _ := rules.ParseCommandExpiry(a.SendCommand.ExpiresAfter)
				if in.ExpiresAt != nil && in.ExpiresAt.Sub(in.At) == expiry {
					c.RelativeExpiry = expiry
					break
				}
			}
			if in.ExpiresAt != nil && c.RelativeExpiry == 0 {
				c.ExpiryLabel = "synthetic"
			}
		case evaluate.KindHTTPCall:
			c.ConfigDigest = actionConfigDigest(s, in)
			disc = in.URL
			c.BodyDigest = digest(in.Body)
			c.Method = in.Method
		case evaluate.KindPublish:
			c.ConfigDigest = actionConfigDigest(s, in)
			disc = in.Connector
			c.BodyDigest = digest(in.Body)
			c.Method = in.Method
		}
		out = append(out, element{groupKey{in.Series, in.Kind, disc}, in.At, c, ref(in.Series, in.At, in.Raise)})
	}
	return out
}
func actionConfigDigest(s side, in evaluate.Intent) string {
	var configs []string
	for _, a := range s.def.Actions {
		if (in.Kind == evaluate.KindHTTPCall && a.Type == rules.ActionHTTPCall && a.HTTPCall != nil && a.HTTPCall.URL == in.URL) ||
			(in.Kind == evaluate.KindPublish && a.Type == rules.ActionPublish && a.Publish != nil && a.Publish.ConnectorRef == in.Connector) {
			raw, _ := json.Marshal(a)
			configs = append(configs, canonicalJSON(string(raw)))
		}
	}
	if len(configs) == 0 {
		return ""
	}
	sort.Strings(configs)
	raw, _ := json.Marshal(configs)
	return digest(string(raw))
}

func changeTime(c Change) time.Time {
	if c.Base == nil {
		return *c.Draft
	}
	if c.Draft == nil || c.Base.Before(*c.Draft) {
		return *c.Base
	}
	return *c.Draft
}

// diff removes exact matches first, then uses the experimental DiffFirings
// two-list walk: pair within the shift window, otherwise consume the earlier one.
func diff(w *WindowResult, b, d side, shift time.Duration) {
	if shift == 0 {
		shift = DefaultShiftWindow
	}
	group := func(es []element) map[groupKey][]element {
		m := map[groupKey][]element{}
		for _, e := range es {
			m[e.key] = append(m[e.key], e)
		}
		for _, list := range m {
			sort.SliceStable(list, func(i, j int) bool {
				if !list[i].at.Equal(list[j].at) {
					return list[i].at.Before(list[j].at)
				}
				return contentKey(list[i].content) < contentKey(list[j].content)
			})
		}
		return m
	}
	bm, dm := group(elements(w.BaseEdges, w.BaseIntents, b)), group(elements(w.DraftEdges, w.DraftIntents, d))
	set := map[groupKey]bool{}
	for k := range bm {
		set[k] = true
	}
	for k := range dm {
		set[k] = true
	}
	var changes []Change
	for k := range set {
		bs, ds := bm[k], dm[k]
		// Remove the exact multiset intersection before pairing changed elements.
		type exactKey struct {
			at      time.Time
			content Content
		}
		remaining := map[exactKey]int{}
		for _, e := range ds {
			remaining[exactKey{e.at.UTC(), e.content}]++
		}
		matched := map[exactKey]int{}
		var baseRest, draftRest []element
		for _, e := range bs {
			key := exactKey{e.at.UTC(), e.content}
			if remaining[key] > 0 {
				remaining[key]--
				matched[key]++
				w.Counts.Unchanged++
			} else {
				baseRest = append(baseRest, e)
			}
		}
		for _, e := range ds {
			key := exactKey{e.at.UTC(), e.content}
			if matched[key] > 0 {
				matched[key]--
			} else {
				draftRest = append(draftRest, e)
			}
		}
		bs, ds = baseRest, draftRest
		i, j := 0, 0
		for i < len(bs) || j < len(ds) {
			c := Change{Device: k.device, Subject: k.subject, Discriminator: k.discriminator}
			switch {
			case i < len(bs) && j < len(ds) && absDuration(ds[j].at.Sub(bs[i].at)) <= shift:
				be, de := bs[i], ds[j]
				i++
				j++
				if be.at.Equal(de.at) && be.content == de.content {
					w.Counts.Unchanged++
					continue
				}
				c.Base = &be.at
				c.Draft = &de.at
				c.BaseContent = &be.content
				c.DraftContent = &de.content
				c.Reading = de.reading
				c.Shift = de.at.Sub(be.at)
				c.ContentChanged = be.content != de.content
				if be.at.Equal(de.at) {
					c.Kind = ContentChanged
					w.Counts.ContentChanged++
				} else {
					c.Kind = Shifted
					w.Counts.Shifted++
				}
			case j >= len(ds) || (i < len(bs) && bs[i].at.Before(ds[j].at)):
				e := bs[i]
				i++
				c.Kind = Removed
				c.Base = &e.at
				c.BaseContent = &e.content
				c.Reading = e.reading
				w.Counts.Removed++
			default:
				e := ds[j]
				j++
				c.Kind = Added
				c.Draft = &e.at
				c.DraftContent = &e.content
				c.Reading = e.reading
				w.Counts.Added++
			}
			for _, dev := range w.Devices {
				if dev.Device == c.Device && dev.Divergence != nil {
					c.AfterDivergence = (c.Base != nil && c.Base.After(*dev.Divergence)) ||
						(c.Draft != nil && c.Draft.After(*dev.Divergence))
				}
			}
			changes = append(changes, c)
		}
	}
	sort.Slice(changes, func(i, j int) bool {
		a, b := changes[i], changes[j]
		if !changeTime(a).Equal(changeTime(b)) {
			return changeTime(a).Before(changeTime(b))
		}
		if a.Device != b.Device {
			return a.Device < b.Device
		}
		if a.Subject != b.Subject {
			return a.Subject < b.Subject
		}
		if a.Discriminator != b.Discriminator {
			return a.Discriminator < b.Discriminator
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if contentKeyValue(a.BaseContent) != contentKeyValue(b.BaseContent) {
			return contentKeyValue(a.BaseContent) < contentKeyValue(b.BaseContent)
		}
		return contentKeyValue(a.DraftContent) < contentKeyValue(b.DraftContent)
	})
	if len(changes) > MaxListedChanges {
		w.ChangesTruncated = true
		changes = changes[:MaxListedChanges]
	}
	w.Changes = changes
}
func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}
func contentKey(c Content) string {
	return c.Severity + "\x00" + c.Payload + "\x00" + c.RelativeExpiry.String() + "\x00" + c.ExpiryLabel + "\x00" + c.BodyDigest + "\x00" + c.Method + "\x00" + c.ConfigDigest
}
func contentKeyValue(c *Content) string {
	if c == nil {
		return ""
	}
	return contentKey(*c)
}
