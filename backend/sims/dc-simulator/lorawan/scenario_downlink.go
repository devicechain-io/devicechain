// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package lorawan

import "time"

// PendingDownlink is one confirmed Class A item snapshotted before an uplink.
type PendingDownlink struct {
	ID       string `json:"id"`
	DevEUI   string `json:"devEui"`
	FPort    int    `json:"fPort"`
	Data     string `json:"data"`
	FCntDown uint32 `json:"fCntDown"`
}

// TakePending removes only items queued before this call. New items wait for the next uplink.
func (d *Downlink) TakePending(eui string) []PendingDownlink {
	items := d.snapshot(eui)
	out := make([]PendingDownlink, 0, len(items))
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, i := range items {
		n := d.counters[i.DevEUI]
		d.counters[i.DevEUI]++
		out = append(out, PendingDownlink{i.id, i.DevEUI, i.FPort, i.Data, n})
	}
	return out
}

// ReportRequests uses the standalone network server's exact txack/ack encoding and queue id.
func ReportRequests(url string, item PendingDownlink, acknowledged bool, at time.Time) []Request {
	ack := "nack"
	if acknowledged {
		ack = "ack"
	}
	d := &Downlink{ack: ack, counters: map[string]uint32{item.DevEUI: item.FCntDown}}
	sum := Summary{Reported: map[string]map[string]int{"txack": {}, "ack": {}}}
	var out []Request
	d.reports(Config{URL: url}, queueItem{DevEUI: item.DevEUI, id: item.ID}, at, func(r Request) int { out = append(out, r); return 202 }, &sum)
	return out
}

// LimitQueued bounds accepted items over this queue's lifetime; zero keeps the standalone default.
func (d *Downlink) LimitQueued(max int) { d.mu.Lock(); defer d.mu.Unlock(); d.maxQueued = max }
