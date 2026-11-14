// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build frontend

// Package frontend runs the browser-state regressions from Go without duplicating React's
// lifecycle in a Go model. This opt-in suite needs the frontend dependencies and packages built:
// cd frontend && npm ci --prefer-offline && npm run build
// cd backend/core && go test -count=1 -tags frontend ./test/frontend
package frontend

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func runStateRegression(t *testing.T, workspace, file, name string) {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate frontend regression harness")
	}
	root, err := filepath.Abs(filepath.Join(filepath.Dir(source), "../../../../frontend"))
	if err != nil {
		t.Fatal(err)
	}
	vitest := filepath.Join(root, "node_modules", "vitest", "vitest.mjs")
	if _, err := os.Stat(vitest); err != nil {
		t.Fatalf("frontend regression dependencies are unavailable: run npm ci --prefer-offline and npm run build in frontend: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", vitest, "run", file, "-t", name)
	cmd.Dir = filepath.Join(root, workspace)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("frontend state regression failed (requires Node and built frontend packages): %v\n%s", err, output)
	}
}

func TestCommandSubjectChange(t *testing.T) {
	runStateRegression(t, "packages/widgets", "src/command-controls.test.tsx", "command subject changes")
}

func TestAlarmStormReconciliation(t *testing.T) {
	runStateRegression(t, "apps/console", "src/routes/alarms/AlarmsPage.triage.test.tsx", "slow alarm reconciles")
}
