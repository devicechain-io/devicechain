// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/devicechain-io/dcctl/internal/tofujson"
	"k8s.io/client-go/kubernetes/fake"
)

func TestStoreGateScaleDownNeverExceedsItsTotal(t *testing.T) {
	dyn := fakeDyn(aCluster("dc-x", "dc-rdb", 1, 3, "Creating a new replica"))
	typed := fake.NewSimpleClientset(cnpgPods("dc-x", "dc-rdb", 3)...)
	p := newProgressRun(t, true, installflow.StepInstallRDBReady)
	if err := waitForCNPGClusterReady(p.ctx, dyn, typed, "dc-x", "dc-rdb", testWaitBound, testWaitPoll); err == nil {
		t.Fatal("an unhealthy Cluster passed the gate")
	}
	ps := progressOf(p.finish(t), metricStoreInstances)
	if len(ps) == 0 {
		t.Fatal("the gate reported nothing")
	}
	for _, sp := range ps {
		if sp.Total == nil || *sp.Total != 1 || sp.Current != 1 {
			t.Errorf("scale-down reported %v, want 1/1", sp)
		}
	}
}

// Exercise the actual retry classifier, with its marker beyond the retained line
// prefix, including JSON escapes and word wrapping across the marker.
func TestOversizedDiagnosticStillClassifiesCNPGAdmission(t *testing.T) {
	for _, field := range []string{"@message", "summary", "detail"} {
		t.Run(field, func(t *testing.T) {
			marker := "failed\n \u2003calling webhook \"mcluster.cnpg.io\""
			d := map[string]string{"severity": "error", "summary": "cannot create Cluster", "detail": "provider failure"}
			m := map[string]any{"type": "diagnostic", "@level": "error", "@message": "Error", "diagnostic": d}
			text := strings.Repeat("z", tofujson.MaxLine+100) + "\uFFFD" + marker + strings.Repeat("z", 100)
			if field == "@message" {
				m[field] = text
			} else {
				d[field] = text
			}
			b, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			b = bytes.ReplaceAll(b, []byte("failed"), []byte(`\u0066ailed`))
			b = bytes.ReplaceAll(b, []byte(".cnpg.io"), []byte(`\u002ecnpg.io`))
			o := tofujson.New(nil, nil, nil)
			for len(b) > 0 {
				n := min(7, len(b))
				_, _ = o.Write(b[:n])
				b = b[n:]
			}
			o.Close()
			if !isCNPGWebhookUnavailable(o.Errors()) {
				t.Fatalf("oversized %s lost the retry classifier's text", field)
			}
			if len(o.Errors()) > tofujson.MaxLine {
				t.Fatal("oversized diagnostic was retained without a bound")
			}
		})
	}
}

// Measure the whole runner, not just Observer.Write: terraform-exec used to hold
// this entire unterminated line before the observer could enforce MaxLine.
func TestObservedApplyBoundsLineMemoryBeforeTheObserver(t *testing.T) {
	// TestMain warms charts in the background on a full suite. Select just this
	// test in a child so TotalAlloc measures the runner rather than those renders.
	const child = "DC_TEST_OBSERVED_LINE_MEMORY"
	if os.Getenv(child) == "" {
		bin, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(bin, "-test.run=^TestObservedApplyBoundsLineMemoryBeforeTheObserver$", "-test.count=1")
		cmd.Env = append(os.Environ(), child+"=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("isolated allocation check: %v\n%s", err, output)
		}
		return
	}
	t.Run("terminated", func(t *testing.T) {
		checkObservedLineMemory(t, "\n"+
			`{"type":"apply_complete","@message":"next message","hook":{"action":"create"}}`+"\n", "next message\n")
	})
	t.Run("unterminated", func(t *testing.T) { checkObservedLineMemory(t, "", "") })
}

func checkObservedLineMemory(t *testing.T, tail, want string) {
	t.Helper()
	testKube(t)
	stream := []byte(strings.Repeat("z", 16<<20) + tail)
	bin, _ := fakeTofuJSON(t, struct {
		stream []byte
		rc     int
	}{stream, 0})
	p := newProgressRun(t, true, installflow.StepInstallPrerequisites)
	tf, err := newTofuExec(t.TempDir(), bin, "")
	if err != nil {
		t.Fatal(err)
	}
	tf.useConsole(out(p.ctx))
	if _, _, err := tf.Version(p.ctx, false); err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	err = tf.Apply(p.ctx)
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatal(err)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 8<<20 {
		t.Errorf("observing a 16 MiB line allocated %d bytes; want under 8 MiB", allocated)
	}
	if p.out.String() != want {
		t.Errorf("lost line framing: %q", p.out.String())
	}
	p.finish(t)
}

func TestObservedApplyPreservesPlainApplyInputs(t *testing.T) {
	testKube(t)
	var plain string
	for _, observe := range []bool{false, true} {
		bin, dir := fakeTofuJSON(t, struct {
			stream []byte
			rc     int
		}{[]byte("done\n"), 0})
		p := newProgressRun(t, observe, installflow.StepInstallPrerequisites)
		tf, err := newTofuExec(t.TempDir(), bin, "")
		if err != nil {
			t.Fatal(err)
		}
		tf.useConsole(out(p.ctx))
		if err := tf.Apply(p.ctx, applyVar("a=spaces and \"quotes\""), applyVar("b=$literal")); err != nil {
			t.Fatal(err)
		}
		argv, err := os.ReadFile(filepath.Join(dir, "apply-1.argv"))
		if err != nil {
			t.Fatal(err)
		}
		if !observe {
			plain = string(argv)
		} else if got := strings.ReplaceAll(string(argv), "-json\n", ""); got != plain {
			t.Errorf("observed apply changed its inputs:\n%s\nplain:\n%s", got, plain)
		}
		p.finish(t)
	}
}
