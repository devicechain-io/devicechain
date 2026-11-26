// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/ota"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// ---------------------------------------------------------------------------
// report — the drill's devices answer for their configuration
// ---------------------------------------------------------------------------
//
// A line settles only on the device's own answer, and the drill's 2,000 devices have no broker sessions.
// So their answers are published where a device's MQTT publish lands: the device-reports stream, on the
// device's own subject ({instance}.{tenant}.device-reports.{token}), through the broker login of the area
// that forwards reports there (event-sources), with a JetStream acknowledgement for every one. What runs
// after that is the platform's own: the shared durable consumer, capture, the state row and settlement.
//
// 🔴 WHAT IT SKIPS, said plainly: the MQTT hop and the device grant. Neither is the subject of this drill
// (the transport rigs own them); what the drill asks is whether a roll that mixes pod versions leaves
// every line provably settled, and the report's path from the stream onward is exactly the one a real
// device's report takes.
//
// Each row is `token<TAB>revision<TAB>digest`: what the rig read for each device from the configuration
// revisions it holds. Every report is validated with the platform's own decoder rules (ota) before it is
// sent, so a malformed row fails here rather than being dead-lettered where nobody reads it.

type bcReportRow struct {
	Token    string
	Revision int64
	Digest   string
}

func parseReportRows(r io.Reader) ([]bcReportRow, error) {
	var rows []bcReportRow
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) != 3 {
			return nil, fmt.Errorf("a report row has %d field(s), want token, revision, digest: %q", len(f), line)
		}
		rev, err := strconv.ParseInt(f[1], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("revision %q: %w", f[1], err)
		}
		rows = append(rows, bcReportRow{Token: f[0], Revision: rev, Digest: f[2]})
	}
	return rows, sc.Err()
}

// bcReportPayload is the config.status APPLIED report a device sends for a revision it applied.
func bcReportPayload(row bcReportRow) ([]byte, error) {
	rep := ota.ConfigStatusReport{V: ota.ReportVersion, Kind: ota.KindConfigStatus, Revision: row.Revision,
		Digest: row.Digest, Status: ota.ConfigApplied}
	if err := rep.Validate(); err != nil {
		return nil, fmt.Errorf("device %s revision %d: %w", row.Token, row.Revision, err)
	}
	return json.Marshal(rep)
}

// bcReportNatsOptions are the connection options for publishing reports as an area's broker login. A
// JetStream publish waits for the stream's acknowledgement on a reply inbox, and an area's login may
// subscribe only to its own inbox prefix (messaging.InboxPrefix); nats.go's default "_INBOX." is refused as a
// permissions violation and every publish then times out.
func bcReportNatsOptions(user, pass, area string) []nats.Option {
	return []nats.Option{nats.UserInfo(user, pass), nats.Name("upgrade-drill-bulkchange-report"), nats.Timeout(15 * time.Second),
		nats.CustomInboxPrefix(messaging.InboxPrefix(area))}
}

func bcReport(ctx context.Context, argv []string, stdout io.Writer) error {
	fs := flagSetFor("bulkchange report")
	var natsURL, user, passEnv, caFile, serverName, instanceID, tenant, rowsFile, area string
	var workers int
	fs.StringVar(&natsURL, "nats", "", "broker URL, e.g. nats://127.0.0.1:14222 (required)")
	fs.StringVar(&user, "user", "", "broker login (required)")
	fs.StringVar(&passEnv, "password-env", "DC_BC_NATS_PASSWORD", "environment variable holding the login's password")
	fs.StringVar(&caFile, "ca", "", "PEM CA the broker's certificate chains to (TLS on)")
	fs.StringVar(&serverName, "server-name", "", "name to verify the broker's certificate against (TLS on)")
	fs.StringVar(&instanceID, "instance-id", "", "the instance id that prefixes every subject (required)")
	fs.StringVar(&tenant, "tenant", "", "the tenant token (required)")
	fs.StringVar(&rowsFile, "rows", "", "file of token<TAB>revision<TAB>digest rows (required)")
	fs.IntVar(&workers, "workers", 8, "concurrent publishes")
	fs.StringVar(&area, "area", "event-sources", "functional area whose broker login --user is: its connections may subscribe only to that area's reply inbox")
	if err := fs.Parse(argv); err != nil {
		return failWith(exitSetup, "%w", err)
	}
	pass := os.Getenv(passEnv)
	if natsURL == "" || user == "" || pass == "" || instanceID == "" || tenant == "" || rowsFile == "" || area == "" || workers < 1 {
		return failWith(exitSetup, "--nats, --user, a password in $%s, --instance-id, --tenant, --rows and --workers >= 1 are required", passEnv)
	}
	f, err := os.Open(rowsFile)
	if err != nil {
		return failWith(exitSetup, "open %s: %w", rowsFile, err)
	}
	rows, err := parseReportRows(f)
	f.Close()
	if err != nil {
		return failWith(exitShape, "%s: %w", rowsFile, err)
	}
	if len(rows) == 0 {
		return failWith(exitShape, "%s holds no report to send", rowsFile)
	}
	payloads := make([][]byte, len(rows))
	for i, r := range rows {
		if payloads[i], err = bcReportPayload(r); err != nil {
			return failWith(exitShape, "%w", err)
		}
	}
	opts := bcReportNatsOptions(user, pass, area)
	if caFile != "" {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return failWith(exitSetup, "read %s: %w", caFile, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return failWith(exitSetup, "%s holds no PEM certificate", caFile)
		}
		opts = append(opts, nats.Secure(&tls.Config{RootCAs: pool, ServerName: serverName, MinVersion: tls.VersionTLS12}))
	}
	nc, err := nats.Connect(natsURL, opts...)
	if err != nil {
		return failWith(exitSetup, "connect to %s as %s: %w", natsURL, user, err)
	}
	defer nc.Close()
	js, err := jetstream.New(nc)
	if err != nil {
		return failWith(exitSetup, "jetstream: %w", err)
	}
	var next atomic.Int64
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := int(next.Add(1)) - 1
				if i >= len(rows) || ctx.Err() != nil {
					return
				}
				subject := messaging.DeviceScopedSubject(instanceID, tenant, streams.DeviceReports, rows[i].Token)
				pctx, cancel := context.WithTimeout(ctx, 15*time.Second)
				_, err := js.Publish(pctx, subject, payloads[i])
				cancel()
				if err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = fmt.Errorf("publish %s: %w", subject, err)
					}
					mu.Unlock()
					return
				}
			}
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return failWith(exitRefused, "%w (a 'no responders' answer means no stream captures that subject: the instance id or the tenant is wrong)", firstErr)
	}
	fmt.Fprintf(stdout, "bulkchange report: %d APPLIED report(s) stored on %s.%s.%s.*\n", len(rows), instanceID, tenant, streams.DeviceReports)
	return nil
}

// ---------------------------------------------------------------------------
// attrload — the P-1 interactive load: ordinary attribute writes at a fixed rate, timed
// ---------------------------------------------------------------------------

// bcLatency is the client-side latency summary of a load run, in milliseconds.
type bcLatency struct {
	Count  int     `json:"count"`
	Errors int     `json:"errors"`
	Busy   int     `json:"busyRetries"`
	P50    float64 `json:"p50Ms"`
	P90    float64 `json:"p90Ms"`
	P99    float64 `json:"p99Ms"`
	Max    float64 `json:"maxMs"`
	Rate   float64 `json:"achievedPerSec"`
}

// percentile is the nearest-rank percentile of sorted durations, in ms; 0 for none.
func percentile(sorted []time.Duration, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	rank := int(math.Ceil(q*float64(len(sorted)))) - 1
	rank = max(0, min(rank, len(sorted)-1))
	return float64(sorted[rank]) / float64(time.Millisecond)
}

func summarizeLatency(d []time.Duration, errs, busy int, elapsed time.Duration) bcLatency {
	s := append([]time.Duration(nil), d...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	out := bcLatency{Count: len(s), Errors: errs, Busy: busy,
		P50: percentile(s, 0.50), P90: percentile(s, 0.90), P99: percentile(s, 0.99)}
	if len(s) > 0 {
		out.Max = float64(s[len(s)-1]) / float64(time.Millisecond)
	}
	if elapsed > 0 {
		out.Rate = float64(len(s)) / elapsed.Seconds()
	}
	return out
}

func bcAttrLoad(ctx context.Context, argv []string, stdout io.Writer) error {
	fs := flagSetFor("bulkchange attrload")
	var c connection
	c.bind(fs)
	var receipt, url, bulk, out string
	var rate, first, last int
	var dur time.Duration
	bindSession(fs, &receipt, &url)
	fs.StringVar(&bulk, "bulk", "", "the bulk-change receipt whose devices are written (required)")
	fs.StringVar(&out, "out", "", "write the latency summary JSON here (required)")
	fs.IntVar(&rate, "rate", 20, "writes per second")
	fs.IntVar(&first, "first", 1, "first device (1-based) written")
	fs.IntVar(&last, "last", 0, "last device written (default: every device)")
	fs.DurationVar(&dur, "duration", 3*time.Minute, "how long to write")
	if err := fs.Parse(argv); err != nil {
		return failWith(exitSetup, "%w", err)
	}
	if out == "" || rate < 1 || dur <= 0 {
		return failWith(exitSetup, "--out, --rate >= 1 and a positive --duration are required")
	}
	b, err := readBulkReceipt(bulk)
	if err != nil {
		return err
	}
	if last == 0 {
		last = len(b.Devices)
	}
	if first < 1 || last > len(b.Devices) || first > last {
		return failWith(exitSetup, "--first %d / --last %d is outside 1..%d", first, last, len(b.Devices))
	}
	devices := b.Devices[first-1 : last]
	s, err := openSession(c, receipt, url)
	if err != nil {
		return err
	}
	var mu sync.Mutex
	var lat []time.Duration
	var errs, busy int
	var wg sync.WaitGroup
	tick := time.NewTicker(time.Second / time.Duration(rate))
	defer tick.Stop()
	start := time.Now()
	end := start.Add(dur)
	for i := 0; time.Now().Before(end) && ctx.Err() == nil; i++ {
		<-tick.C
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			t0 := time.Now()
			retries, err := bcSetAttr(ctx, s, devices[i%len(devices)], b.WarmKey, fmt.Sprintf("load-%d", i))
			d := time.Since(t0)
			mu.Lock()
			defer mu.Unlock()
			busy += retries
			if err != nil {
				errs++
				return
			}
			lat = append(lat, d)
		}(i)
	}
	wg.Wait()
	sum := summarizeLatency(lat, errs, busy, time.Since(start))
	body, err := json.MarshalIndent(sum, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(out, body, 0o600); err != nil {
		return failWith(exitSetup, "write %s: %w", out, err)
	}
	fmt.Fprintf(stdout, "bulkchange attrload: %d write(s), %d error(s), p50 %.1f ms, p99 %.1f ms\n", sum.Count, sum.Errors, sum.P50, sum.P99)
	if sum.Errors != 0 {
		return failWith(exitMismatch, "%d interactive write(s) failed, so the latency is of a different load", sum.Errors)
	}
	return nil
}
