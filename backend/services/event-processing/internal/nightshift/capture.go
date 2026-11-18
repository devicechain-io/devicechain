// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package nightshift is file-backed test support for the recorded Night Shift acceptance tests.
// Production code must not import it.
package nightshift

import (
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
)

const Tenant = "nightshift"

type Header struct {
	Epoch                        time.Time
	ProfileToken, ProfileVersion string
	R1                           struct {
		Token      string
		Definition json.RawMessage
	}
	Windows   map[string][2]time.Time
	Devices   []string
	Bystander string
}
type Sample struct {
	Kind     string
	Seq      uint64
	Device   string
	At       time.Time
	Metrics  map[string]float64
	RaisedAt time.Time
}
type Capture struct {
	Header           Header
	Samples, Oracles []Sample
	Messages         []messaging.Message
	Opens            int
}

func Load(t testing.TB) *Capture {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	dir := filepath.Join(filepath.Dir(file), "..", "review", "testdata")
	f, err := os.Open(filepath.Join(dir, "night-shift.capture.jsonl.gz"))
	must(t, err)
	defer f.Close()
	z, err := gzip.NewReader(f)
	must(t, err)
	defer z.Close()
	raw, err := io.ReadAll(z)
	must(t, err)
	hash, err := os.ReadFile(filepath.Join(dir, "night-shift.capture.sha256"))
	must(t, err)
	if fmt.Sprintf("%x", sha256.Sum256(raw)) != strings.TrimSpace(string(hash)) {
		t.Fatal("Night Shift capture hash mismatch")
	}
	c := &Capture{}
	scan := bufio.NewScanner(strings.NewReader(string(raw)))
	if !scan.Scan() {
		t.Fatal("missing capture header")
	}
	must(t, json.Unmarshal(scan.Bytes(), &c.Header))
	for scan.Scan() {
		var s Sample
		must(t, json.Unmarshal(scan.Bytes(), &s))
		if s.Kind == "oracle" {
			c.Oracles = append(c.Oracles, s)
			continue
		}
		if s.Kind != "event" {
			t.Fatalf("unknown capture line %s", s.Kind)
		}
		c.Samples = append(c.Samples, s)
		var entries []resolvedevent.ResolvedMeasurementEntry
		keys := make([]string, 0, len(s.Metrics))
		for k := range s.Metrics {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			entries = append(entries, resolvedevent.ResolvedMeasurementEntry{Name: k, Value: strconv.FormatFloat(s.Metrics[k], 'g', -1, 64)})
		}
		b, err := resolvedevent.MarshalResolvedEvent(&resolvedevent.ResolvedEvent{Source: "nightshift", SourceDeviceToken: s.Device, ProfileVersionToken: c.Header.ProfileVersion, OccurredTime: s.At, ProcessedTime: s.At, EventType: eventtype.Measurement, Payload: &resolvedevent.ResolvedMeasurementsPayload{Entries: []resolvedevent.ResolvedMeasurementsEntry{{OccurredTime: s.At, Entries: entries}}}})
		must(t, err)
		c.Messages = append(c.Messages, messaging.Message{Subject: "dc." + Tenant + ".resolved-events", Value: b, StreamSeq: s.Seq, AppendTime: s.At})
	}
	must(t, scan.Err())
	if len(c.Messages) == 0 || len(c.Oracles) == 0 {
		t.Fatal("empty Night Shift evidence")
	}
	return c
}
func must(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func (c *Capture) NewReplayReaderFromTime(_ string, start time.Time) (messaging.ReplayReader, time.Time, error) {
	c.Opens++
	i := sort.Search(len(c.Messages), func(i int) bool { return !c.Messages[i].AppendTime.Before(start) })
	return &reader{messages: c.Messages[i:]}, c.Messages[0].AppendTime, nil
}

type reader struct{ messages []messaging.Message }

func (r *reader) Read(ctx context.Context) (messaging.Message, error) {
	if err := ctx.Err(); err != nil {
		return messaging.Message{}, err
	}
	if len(r.messages) == 0 {
		return messaging.Message{}, io.EOF
	}
	m := r.messages[0]
	r.messages = r.messages[1:]
	return m, nil
}
func (*reader) Close() error { return nil }
