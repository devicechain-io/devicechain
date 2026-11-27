// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/devicechain-io/dcctl/internal/installflow"
)

// 🔴 A RESTORE SOURCE'S USERINFO IS A SECRET OF THE RUN. A tool that echoes
// s3://user:password@bucket into its output reaches the event stream redacted.
func TestRegisterSecretsCoversARestoreURLsUserinfo(t *testing.T) {
	const pw = "dcCanaryRestorePw_93Kq"
	var mu sync.Mutex
	var seen []string
	hub, err := installflow.NewHub(installflow.Options{Operation: installflow.OperationInstall,
		Sink: installflow.SinkFunc(func(e installflow.Envelope) {
			if l, ok := e.Payload.(installflow.LogLine); ok {
				mu.Lock()
				seen = append(seen, l.Text.String())
				mu.Unlock()
			}
		})})
	if err != nil {
		t.Fatal(err)
	}
	var opts InstallOptions
	opts.Restore = RestorePlan{RdbFrom: "s3://dcuser:" + pw + "@bucket/archive"}
	registerSecrets(hub, opts, io.Discard)
	if err := hub.Emit(installflow.Event{Payload: installflow.RunStarted{
		Target:   installflow.Target{Mode: "kind-create", Context: installflow.Text("kind-devicechain")},
		Manifest: []installflow.ManifestStep{{ID: installflow.StepInstallInputs}},
	}}); err != nil {
		t.Fatal(err)
	}
	w, err := hub.LogWriter("", "tofu", installflow.LogInfo)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(w, "restoring from s3://dcuser:"+pw+"@bucket/archive\n")
	_ = w.Close()
	if err := hub.Finish(installflow.Summary{Outcome: installflow.RunFailed, InstallRecord: installflow.RecordNotRead,
		Readiness: installflow.ReadinessNotChecked, Claim: installflow.ClaimNotClaimed,
		PartialState: installflow.EffectNoneObserved}); err != nil {
		t.Fatal(err)
	}
	<-hub.Done()
	mu.Lock()
	defer mu.Unlock()
	// The log ring is what a presenter draws log lines from; the sink, what it is sent.
	for _, l := range hub.Logs().Lines {
		seen = append(seen, l.Text.String())
	}
	if len(seen) == 0 {
		t.Fatal("no log line reached the stream")
	}
	for _, l := range seen {
		if strings.Contains(l, pw) {
			t.Fatalf("the restore password reached the event stream: %q", l)
		}
	}
	if strings.Contains(hub.Redact("x "+pw), pw) {
		t.Fatal("the hub does not redact the restore password")
	}
}
