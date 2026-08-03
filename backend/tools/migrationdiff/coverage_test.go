// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/tenantpurge"
)

// TestPopulationGuardFiresOnAnEmptyPlan is the guard's own negative control.
//
// It exists because "no unclassified tables" is also what a plan containing NOTHING
// says — a connection to the wrong database, a catalog query that silently returned no
// rows, a migration loop that ran zero areas. Every one of those produces a green
// coverage run that checked nothing, and green is exactly what a broken gate looks like.
// This proves the guard can fail; without it the guard is a comment.
func TestPopulationGuardFiresOnAnEmptyPlan(t *testing.T) {
	err := assertPlanIsPopulated(&tenantpurge.Plan{}, areas)
	if err == nil {
		t.Fatal("an empty plan must not be reported as clean coverage")
	}
	for _, a := range areas {
		if !strings.Contains(err.Error(), a.name) {
			t.Errorf("the message must name every area that contributed nothing; %q is missing", a.name)
		}
	}
}

// TestPopulationGuardPassesWhenEveryAreaContributed pins the other direction, so the
// guard cannot be satisfied by simply always failing.
func TestPopulationGuardPassesWhenEveryAreaContributed(t *testing.T) {
	plan := &tenantpurge.Plan{}
	for _, a := range areas {
		plan.Entries = append(plan.Entries, tenantpurge.Entry{
			Table: tenantpurge.Table{Schema: a.name, Name: "something"},
			Class: tenantpurge.ClassDirect,
		})
	}
	if err := assertPlanIsPopulated(plan, areas); err != nil {
		t.Fatalf("a fully populated plan must pass: %v", err)
	}
}

// TestPopulationGuardFiresWhenOneAreaIsMissing is the case that actually distinguishes
// this guard from a row count: nine of ten areas present still means the tenth was
// never looked at.
func TestPopulationGuardFiresWhenOneAreaIsMissing(t *testing.T) {
	plan := &tenantpurge.Plan{}
	for _, a := range areas[:len(areas)-1] {
		plan.Entries = append(plan.Entries, tenantpurge.Entry{
			Table: tenantpurge.Table{Schema: a.name, Name: "something"},
			Class: tenantpurge.ClassDirect,
		})
	}
	missing := areas[len(areas)-1].name
	err := assertPlanIsPopulated(plan, areas)
	if err == nil {
		t.Fatalf("a plan missing %s entirely must not pass", missing)
	}
	if !strings.Contains(err.Error(), missing) {
		t.Errorf("the message must name %s; got %q", missing, err.Error())
	}
}

// TestTheFailureMessageNamesTheTableAndTheWayOut pins what a maintainer sees when their
// migration trips the gate. They have just added a table and have no reason to know
// this package exists, so a message that only says "coverage failed" costs them an hour.
func TestTheFailureMessageNamesTheTableAndTheWayOut(t *testing.T) {
	plan := &tenantpurge.Plan{Entries: []tenantpurge.Entry{{
		Table: tenantpurge.Table{Schema: "device-management", Name: "rogue_readings"},
		Class: tenantpurge.ClassUnclassified,
	}}}

	err := unclassifiedError(plan)
	if err == nil {
		t.Fatal("an unclassified table must fail coverage")
	}
	for _, want := range []string{
		"device-management.rogue_readings", // which table
		"tenant_id",                        // option 1
		"foreign key",                      // option 2
		"tenantpurge/exempt.go",            // option 3, with the file to edit
		"ClassDeferred",                    // and the option for a known hole
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the failure message is missing %q:\n%s", want, err.Error())
		}
	}
}

// TestACleanPlanProducesNoError pins the other direction of the same function.
func TestACleanPlanProducesNoError(t *testing.T) {
	plan := &tenantpurge.Plan{Entries: []tenantpurge.Entry{{
		Table: tenantpurge.Table{Schema: "device-management", Name: "devices"},
		Class: tenantpurge.ClassDirect,
	}}}
	if err := unclassifiedError(plan); err != nil {
		t.Fatalf("a fully classified plan must pass: %v", err)
	}
}
