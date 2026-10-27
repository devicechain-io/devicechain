// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"maps"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"testing"

	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/releaseutil"
)

// 🔑 A CLIENT-SIDE RENDER IS PAID ONCE PER (CHART, VALUES) PAIR PER BINARY, NOT ONCE PER CALL.
//
// renderChartClientSide is a pure function of the chart and the values it is handed (its own
// comment says so, and instanceConfigFromManifest's pre-flight depends on it). Under the race
// detector one render of the whole chart costs on the order of a CPU-second, and the suites that
// range over renderProfiles (the front-end matrix, the gateway parity, the chain checks, the
// API-server installs) each render the same pairs again: the parity test alone rendered every
// profile four times, twice of them identical. Measured under -race on four CPUs the package
// had grown to ~840 s, and the renders were most of it.
//
// The memo is keyed on CONTENT, never on a pointer: a hash of the chart (templates, schema,
// values, files, metadata, subcharts) and of the values encoded as JSON. A test that edits a
// chart or hands in different values gets its own render, which is what keeps the negative
// controls meaningful: they change the input, so they miss. What is shared is the returned
// string, which is immutable.
//
// Concurrent callers of one key wait for the first rather than each rendering (the parallel
// subtests start together, so without that the memo would save nothing). A context error is
// never remembered: a cancelled render says nothing about the pair.
type renderMemoEntry struct {
	once sync.Once
	out  string
	err  error
}

var (
	renderMemoMu      sync.Mutex
	renderMemo        = map[[sha256.Size]byte]*renderMemoEntry{}
	renderMemoRenders atomic.Int64 // renders actually performed, for the memo's own test
)

func memoizedClientSideRender(ctx context.Context, ch *chart.Chart, vals map[string]interface{}) (string, error) {
	key, ok := renderMemoKey(ch, vals)
	if !ok {
		// Values that cannot be encoded cannot be keyed: render them as they are.
		return renderChartClientSideUncached(ctx, ch, vals)
	}

	renderMemoMu.Lock()
	e, ok := renderMemo[key]
	if !ok {
		e = &renderMemoEntry{}
		renderMemo[key] = e
	}
	renderMemoMu.Unlock()

	e.once.Do(func() {
		renderMemoRenders.Add(1)
		e.out, e.err = renderChartClientSideUncached(ctx, ch, vals)
		if e.err != nil && (errors.Is(e.err, context.Canceled) || errors.Is(e.err, context.DeadlineExceeded)) {
			// Forget it: the next caller renders for itself.
			renderMemoMu.Lock()
			delete(renderMemo, key)
			renderMemoMu.Unlock()
		}
	})
	return e.out, e.err
}

// memoizedInstanceConfigDocument is the same memo for the instance configuration document the
// chart renders: picking it out of the manifest stream (a split of ~1 MB and a decode of each
// piece) costs about half what the render does, and every profileValues call pays it.
type docMemoEntry struct {
	once sync.Once
	out  []byte
	err  error
}

var (
	docMemoMu sync.Mutex
	docMemo   = map[[sha256.Size]byte]*docMemoEntry{}
)

func memoizedInstanceConfigDocument(ctx context.Context, ch *chart.Chart, vals map[string]interface{}) ([]byte, error) {
	key, ok := renderMemoKey(ch, vals)
	if !ok {
		return renderInstanceConfigDocumentUncached(ctx, ch, vals)
	}
	docMemoMu.Lock()
	e, found := docMemo[key]
	if !found {
		e = &docMemoEntry{}
		docMemo[key] = e
	}
	docMemoMu.Unlock()
	e.once.Do(func() {
		e.out, e.err = renderInstanceConfigDocumentUncached(ctx, ch, vals)
		if e.err != nil && (errors.Is(e.err, context.Canceled) || errors.Is(e.err, context.DeadlineExceeded)) {
			docMemoMu.Lock()
			delete(docMemo, key)
			docMemoMu.Unlock()
		}
	})
	// A copy, so a caller that edits the bytes edits its own.
	return slices.Clone(e.out), e.err
}

// renderMemoKey is the content key: the chart and the values, both by hash.
func renderMemoKey(ch *chart.Chart, vals map[string]interface{}) ([sha256.Size]byte, bool) {
	var key [sha256.Size]byte
	vb, err := json.Marshal(vals)
	if err != nil {
		return key, false
	}
	h := sha256.New()
	hashChart(h, ch)
	h.Write(vb)
	h.Sum(key[:0])
	return key, true
}

// splitManifests is releaseutil.SplitManifests, remembered by manifest. Helm's splitter is a
// regular expression run over the whole stream, and under the race detector that costs about a
// quarter of a CPU-second per ~1 MB manifest (a seventh of this package's whole budget when
// every reader of a rendered manifest splits it for itself). A render is remembered above, so
// the readers of one render all see the same string; this gives them the same split. The map
// is a copy, so a caller that edits it edits its own.
var (
	splitMemoMu sync.Mutex
	splitMemo   = map[string]*splitMemoEntry{}
)

type splitMemoEntry struct {
	once  sync.Once
	parts map[string]string
}

func splitManifests(manifest string) map[string]string {
	splitMemoMu.Lock()
	e, ok := splitMemo[manifest]
	if !ok {
		e = &splitMemoEntry{}
		splitMemo[manifest] = e
	}
	splitMemoMu.Unlock()
	e.once.Do(func() { e.parts = releaseutil.SplitManifests(manifest) })
	return maps.Clone(e.parts)
}

// hashChart writes everything about ch that can change a render into h.
func hashChart(h interface{ Write([]byte) (int, error) }, ch *chart.Chart) {
	if ch == nil {
		return
	}
	str := func(s string) {
		_, _ = h.Write([]byte(s))
		_, _ = h.Write([]byte{0})
	}
	if ch.Metadata != nil {
		mb, _ := json.Marshal(ch.Metadata)
		_, _ = h.Write(mb)
	}
	for _, f := range ch.Templates {
		str(f.Name)
		_, _ = h.Write(f.Data)
		_, _ = h.Write([]byte{0})
	}
	for _, f := range ch.Files {
		str(f.Name)
		_, _ = h.Write(f.Data)
		_, _ = h.Write([]byte{0})
	}
	_, _ = h.Write(ch.Schema)
	vb, _ := json.Marshal(ch.Values)
	_, _ = h.Write(vb)
	for _, dep := range ch.Dependencies() {
		str("dep")
		hashChart(h, dep)
	}
}

// warmRenderMemo renders, in the background, what the matrix suites will ask for, so the cores
// the sequential tests leave idle do the work the parallel ones then find done.
//
// 🔑 WHY THIS EXISTS. `go test` runs every sequential test first, one at a time, and holds the
// parallel ones until the last of them returns. On this package that is about a third of the
// run, spent on one or two cores with the rest idle, and the parallel tests that follow are the
// ones that saturate all four (measured under -race on four CPUs: the sequential phase 193 s of
// 578 s). The renders the matrix suites share are a pure function of values the suites build
// from renderProfiles, so they can be paid for during that phase.
//
// It calls the SAME value-building helpers the suites call (profileValuesE,
// frontEndVariantValues, routingVariantValues), so it asks for exactly the keys they ask for,
// and the memo makes a miss and a hit the same answer: if a suite's inputs drift from these,
// the cost is the warm-up's wasted work, never a wrong result. Errors are not reported here;
// the memo holds them and the suite that asks reports them.
//
// Only on a full run. With -run the caller wants those tests, not the matrix, and the child
// run that kubeisolation starts for one test must not spend its cores on it.
func warmRenderMemo() {
	if !fullRun() {
		return
	}
	// Half the cores: the sequential tests running beside this need one, and the broker
	// credential warm-up another.
	workers := max(1, runtime.GOMAXPROCS(0)/2)
	jobs := make(chan func(*chart.Chart))
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ch, err := loadEmbeddedChart()
			if err != nil {
				return
			}
			for job := range jobs {
				job(ch)
			}
		}()
	}
	ctx := context.Background()
	render := func(ch *chart.Chart, vals map[string]interface{}, err error) {
		if err == nil {
			_, _ = renderChartClientSide(ctx, ch, vals)
		}
	}
	for i, p := range renderProfiles {
		jobs <- func(ch *chart.Chart) {
			vals, err := profileValuesE(ctx, ch, p, profileInstance(i))
			render(ch, vals, err)
		}
	}
	for i, p := range renderProfiles {
		for _, v := range frontEndVariants {
			jobs <- func(ch *chart.Chart) {
				vals, err := frontEndVariantValues(ctx, ch, p, i, v)
				render(ch, vals, err)
			}
		}
	}
	for i, p := range renderProfiles {
		for _, controller := range []string{"nginx", "gateway"} {
			for _, tlsOn := range []bool{true, false} {
				jobs <- func(ch *chart.Chart) {
					vals, err := routingVariantValues(ctx, ch, p, profileInstance(i), controller, tlsOn, "")
					render(ch, vals, err)
				}
			}
		}
	}
	close(jobs)
	wg.Wait()
}

// fullRun reports whether this run selects every test (no -run). The background warm-ups are for
// that run only: a caller who names a test wants that test, and the child run kubeisolation
// starts for one must not spend its cores on the matrix.
func fullRun() bool {
	f := flag.Lookup("test.run")
	return f == nil || f.Value.String() == ""
}

// TestTheRenderMemoKeysOnContent is the counterweight to the memo: the same pair is rendered
// once, and a changed value, a changed chart or a changed template each render again. A memo
// keyed on anything coarser would let a test that edits its input read the previous answer.
func TestTheRenderMemoKeysOnContent(t *testing.T) {
	ch := chartForTest(t)
	vals := func(host string) map[string]interface{} {
		v := profileValues(t, ch, renderProfiles[0], "memo1")
		ing, _ := v["ingress"].(map[string]interface{})
		if ing == nil {
			t.Fatalf("the install values carry no ingress block: %v", v)
		}
		ing["host"] = host
		return v
	}
	// Compose every input first: composing the values is itself a render.
	a1v, a2v, bv, ev := vals("memo-a.example.io"), vals("memo-a.example.io"), vals("memo-b.example.io"), vals("memo-a.example.io")
	before := renderMemoRenders.Load()
	a1, err := renderChartClientSide(t.Context(), ch, a1v)
	if err != nil {
		t.Fatal(err)
	}
	a2, err := renderChartClientSide(t.Context(), chartForTest(t), a2v)
	if err != nil {
		t.Fatal(err)
	}
	if a1 != a2 {
		t.Fatal("the same chart and values rendered two different manifests")
	}
	if n := renderMemoRenders.Load() - before; n != 1 {
		t.Fatalf("the same pair rendered %d times, want 1", n)
	}
	b, err := renderChartClientSide(t.Context(), ch, bv)
	if err != nil {
		t.Fatal(err)
	}
	if b == a1 {
		t.Fatal("a different host rendered the same manifest: the memo is not keyed on the values")
	}
	edited := chartForTest(t)
	edited.Templates[0].Data = append(append([]byte{}, edited.Templates[0].Data...), []byte("\n# memo probe\n")...)
	before = renderMemoRenders.Load()
	if _, err := renderChartClientSide(t.Context(), edited, ev); err != nil {
		t.Fatal(err)
	}
	if renderMemoRenders.Load()-before != 1 {
		t.Fatal("an edited template was served from the memo: the key does not cover the chart")
	}
}
