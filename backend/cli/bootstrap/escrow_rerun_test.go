// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dcctl/internal/installflow"
)

func TestExistingEscrowPlanAsksOnceAndOpensBeforeCluster(t *testing.T) {
	isolateEscrow(t)
	path := filepath.Join(t.TempDir(), "prod.escrow")
	if err := WriteEscrow(EscrowPlan{Path: path, Passphrase: "original"}, testRootKey, "prod", time.Now()); err != nil {
		t.Fatal(err)
	}
	var question escrowQuestion
	plan, err := ResolveEscrowPlan("prod", EscrowFlags{File: path, atTerminal: func() bool { return true },
		ask: func(q escrowQuestion) (string, error) { question = q; return "original", nil }})
	if err != nil || plan.Passphrase != "original" {
		t.Fatalf("existing plan: %v", err)
	}
	if question.confirm || question.code != installflow.QuestionEscrowExistingPassphrase ||
		question.purpose != "Passphrase of the existing escrow at "+path {
		t.Errorf("existing artifact asked as a new one: %+v", question)
	}
	clusterCalled := false
	e := &BootstrapEngine{Instance: "prod", SkipPreflight: true, Escrow: EscrowFlags{File: path},
		PersonAtTerminal: func() bool { return true },
		Target: func(context.Context, Console, EscrowPlan) (*State, error) {
			clusterCalled = true
			return nil, errors.New("stop after escrow")
		}}
	r := runEngineRaw(t, e, installflow.Request{}, true, secretReply("original"))
	if !clusterCalled || len(r.replies.Questions()) != 1 {
		t.Fatalf("existing escrow must ask once and reach cluster with the original passphrase: %v", r.err)
	}
	if _, err := ResolveEscrowPlan("prod", EscrowFlags{File: path, DryRun: true,
		ask: func(escrowQuestion) (string, error) {
			t.Fatal("dry run asked for passphrase")
			return "", nil
		}}); err != nil {
		t.Fatalf("dry run must not open an existing artifact: %v", err)
	}
	for _, kind := range []string{"wrong passphrase", "damaged ciphertext", "invalid artifact"} {
		t.Run(kind, func(t *testing.T) {
			pass := "wrong"
			if kind == "damaged ciphertext" {
				damageEscrowCiphertext(t, path)
				pass = "original"
			}
			if kind == "invalid artifact" {
				if err := os.WriteFile(path, []byte("not an escrow"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv(EscrowPassphraseEnv, pass)
			clusterCalled := false
			e := &BootstrapEngine{Instance: "prod", SkipPreflight: true, Escrow: EscrowFlags{File: path},
				Target: func(context.Context, Console, EscrowPlan) (*State, error) {
					clusterCalled = true
					return nil, errors.New("cluster was reached")
				}}
			r := runEngineRaw(t, e, installflow.Request{}, true)
			if r.err == nil || clusterCalled || !strings.Contains(r.err.Error(), path) {
				t.Fatalf("must refuse existing escrow before cluster: called=%t, err=%v", clusterCalled, r.err)
			}
		})
	}
}

func TestEscrowAdviceRequestsOpenVerification(t *testing.T) {
	isolateEscrow(t)
	path := filepath.Join(t.TempDir(), "prod.escrow")
	if err := WriteEscrow(EscrowPlan{Path: path, Passphrase: "original"}, testRootKey, "prod", time.Now()); err != nil {
		t.Fatal(err)
	}
	_, err := ReconcileEscrow(EscrowPlan{Path: path, Passphrase: "wrong"}, testRootKey, "prod", time.Now())
	if err == nil || !strings.Contains(err.Error(), "--instance prod --open") {
		t.Fatalf("bootstrap advice must actually check recoverability: %v", err)
	}
	printed := captureStdout(t, func() {
		reconcileUpgradeEscrow(aBootstrapOf("prod"), testRootKey, anUpgradeWithEscrowAt(path, ""))
	})
	if !strings.Contains(printed, "--instance prod --open") {
		t.Fatalf("upgrade advice must actually check recoverability: %s", printed)
	}
}
