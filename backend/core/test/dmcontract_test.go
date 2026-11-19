// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// plantDMTree writes a minimal workspace: a contract package, a model package, a proto
// package that encodes the contract, and a consumer that uses both correctly. extra is
// planted on top, overriding a file of the same path.
func plantDMTree(t *testing.T, extra map[string]string) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"backend/services/device-management/contract/c.go": `package contract
type AlarmStateChangeEvent struct{}
const AlarmEventRaised = "RAISED"
func Marshal() {}`,
		"backend/services/device-management/model/m.go": `package model
import "github.com/devicechain-io/dc-device-management/contract"
type AlarmStateChangeEvent = contract.AlarmStateChangeEvent
type Api struct{}`,
		"backend/services/device-management/proto/p.go": `package proto
import "github.com/devicechain-io/dc-device-management/contract"
var _ contract.AlarmStateChangeEvent`,
		"backend/services/device-management/model/m_test.go": `package model
import "github.com/devicechain-io/dc-device-management/model"`,
		"backend/services/consumer/c.go": `package consumer
import (
	dm "github.com/devicechain-io/dc-device-management/contract"
	"github.com/devicechain-io/dc-device-management/proto"
)
var _ dm.AlarmStateChangeEvent
var _ = proto.X`,
		"backend/services/consumer/fixture_test.go": `package consumer
import dmmodel "github.com/devicechain-io/dc-device-management/model"
var _ dmmodel.Api
var _ dmmodel.AlarmStateChangeEvent`,
		"backend/services/consumer/sanctioned.go": `package consumer
import dmmodel "github.com/devicechain-io/dc-device-management/model"
var _ dmmodel.Api`,
		"_legacy/old.go": `package old
import "github.com/devicechain-io/dc-device-management/model"
var _ model.AlarmStateChangeEvent`,
	}
	for k, v := range extra {
		files[k] = v
	}
	for rel, src := range files {
		if src == "" {
			continue // an empty value deletes a default file
		}
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func plantedDMLedger() map[string]DMModelSanction {
	return map[string]DMModelSanction{
		"backend/services/consumer/sanctioned.go": {Names: []string{"Api"}, Reason: "planted fixture"},
	}
}

func dmIssues(t *testing.T, root string, ledger map[string]DMModelSanction) []string {
	t.Helper()
	scan, err := scanDMContractBoundary(root)
	if err != nil {
		t.Fatal(err)
	}
	return dmContractBoundaryErrors(scan, ledger)
}

// The clean tree passes: a contract consumer, a ledgered importer, a test fixture that needs
// model, model's own tests and the archived tree are all allowed.
func TestDMContractGuardPassesTheCleanPlantedTree(t *testing.T) {
	if issues := dmIssues(t, plantDMTree(t, nil), plantedDMLedger()); len(issues) != 0 {
		t.Fatalf("clean planted tree failed: %v", issues)
	}
}

// Every planted defect must be reported against the unchanged ledger, and by the message
// that names it. This is the guard's negative control.
func TestDMContractGuardRejectsPlantedDefects(t *testing.T) {
	for _, tc := range []struct {
		name   string
		plant  map[string]string
		ledger func(map[string]DMModelSanction)
		want   string
	}{
		{"new production importer", map[string]string{"backend/services/other/o.go": `package other
import "github.com/devicechain-io/dc-device-management/model"
var _ model.AlarmStateChangeEvent`}, nil, "backend/services/other/o.go imports"},
		{"blank import", map[string]string{"backend/services/other/o.go": `package other
import _ "github.com/devicechain-io/dc-device-management/model"`}, nil, "<blank or dot import>"},
		{"dot import", map[string]string{"backend/services/other/o.go": `package other
import . "github.com/devicechain-io/dc-device-management/model"
var _ Api`}, nil, "<blank or dot import>"},
		{"sanctioned file widens its use", map[string]string{"backend/services/consumer/sanctioned.go": `package consumer
import dmmodel "github.com/devicechain-io/dc-device-management/model"
var _ dmmodel.Api
var _ dmmodel.AlarmStateChangeEvent`}, nil, "model names used changed"},
		{"stale ledger entry", map[string]string{"backend/services/consumer/sanctioned.go": `package consumer`}, nil, "stale ledger entry"},
		{"ledger entry without a reason", nil, func(l map[string]DMModelSanction) {
			l["backend/services/consumer/sanctioned.go"] = DMModelSanction{Names: []string{"Api"}}
		}, "needs its names and a reason"},
		{"test uses only contract names", map[string]string{"backend/services/consumer/wire_test.go": `package consumer
import dmmodel "github.com/devicechain-io/dc-device-management/model"
var _ dmmodel.AlarmStateChangeEvent
var _ = dmmodel.AlarmEventRaised`}, nil, "uses only names contract exports"},
		{"proto reaches model", map[string]string{"backend/services/device-management/proto/p.go": `package proto
import "github.com/devicechain-io/dc-device-management/model"
var _ model.AlarmStateChangeEvent`}, nil, "proto -> github.com/devicechain-io/dc-device-management/model"},
		{"model reached in two hops", map[string]string{
			"backend/services/device-management/proto/p.go": `package proto
import "github.com/devicechain-io/dc-device-management/internal/mid"`,
			"backend/services/device-management/internal/mid/mid.go": `package mid
import "github.com/devicechain-io/dc-device-management/model"`,
		}, nil, "proto -> github.com/devicechain-io/dc-device-management/internal/mid -> github.com/devicechain-io/dc-device-management/model"},
		{"contract imports device-management", map[string]string{"backend/services/device-management/contract/bad.go": `package contract
import "github.com/devicechain-io/dc-device-management/config"`}, nil, "the contract must import nothing of device-management"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ledger := plantedDMLedger()
			if tc.ledger != nil {
				tc.ledger(ledger)
			}
			issues := dmIssues(t, plantDMTree(t, tc.plant), ledger)
			if !strings.Contains(strings.Join(issues, "\n"), tc.want) {
				t.Fatalf("planted defect passed the guard (want an issue containing %q), got: %v", tc.want, issues)
			}
		})
	}
}

// A walk that never reaches the contract package must fail, not pass every test file.
func TestDMContractGuardRefusesATreeWithoutTheContract(t *testing.T) {
	root := plantDMTree(t, map[string]string{"backend/services/device-management/contract/c.go": ""})
	if _, err := scanDMContractBoundary(root); err == nil {
		t.Fatal("a tree with no contract package scanned cleanly")
	}
}
