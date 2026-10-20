// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/devicechain-io/dc-microservice/calcfield"
)

// CalculatedPlanDoc is a published profile version's calculated metrics as the
// device-management plan door returns them, which is also what a backfill job stores,
// verbatim, as its frozen plan. Its JSON names are the door's GraphQL field names.
//
// It is declared here, not imported, so this service does not depend on the one that owns the
// plan: the door's response is the contract, and a test in the backfill package validates the
// document the worker sends against that schema.
type CalculatedPlanDoc struct {
	VersionToken string                 `json:"versionToken"`
	Version      int64                  `json:"version"`
	ActiveSince  *string                `json:"activeSince"`
	Measured     []CalculatedPlanInput  `json:"measured"`
	Calculated   []CalculatedPlanMetric `json:"calculated"`
	ConstantKeys []string               `json:"constantKeys"`
}

// CalculatedPlanInput is one measured metric a formula may read.
type CalculatedPlanInput struct {
	Key      string `json:"key"`
	DataType string `json:"dataType"`
}

// CalculatedPlanMetric is one calculated metric of the version.
type CalculatedPlanMetric struct {
	Key                 string   `json:"key"`
	Expression          string   `json:"expression"`
	DataType            string   `json:"dataType"`
	MinValue            *float64 `json:"minValue"`
	MaxValue            *float64 `json:"maxValue"`
	Unit                *string  `json:"unit"`
	Classifier          float64  `json:"classifier"`
	NewInVersion        bool     `json:"newInVersion"`
	ChangedSinceEarlier bool     `json:"changedSinceEarlier"`
}

// ParseCalculatedPlanDoc decodes a stored plan, refusing unknown fields: a frozen plan the
// worker cannot read exactly is not one to run.
func ParseCalculatedPlanDoc(raw []byte) (*CalculatedPlanDoc, error) {
	var doc CalculatedPlanDoc
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("decoding the frozen plan: %w", err)
	}
	return &doc, nil
}

// Compile builds the calcfield.Plan the doc describes, exactly as the resolver compiles the
// version's metrics: the measured metrics are the inputs, the calculated ones the definitions.
// A metric whose formula cannot run is a Step with an Err, as there; the caller learns it from
// Plan.Closure.
func (d *CalculatedPlanDoc) Compile() (*calcfield.Plan, error) {
	measured := make([]calcfield.Input, 0, len(d.Measured))
	for _, m := range d.Measured {
		t, ok := calcfield.ParseType(m.DataType)
		if !ok {
			return nil, fmt.Errorf("measured metric %q has the data type %q, which no formula can read", m.Key, m.DataType)
		}
		measured = append(measured, calcfield.Input{Key: m.Key, Type: t})
	}
	defs := make([]calcfield.Definition, 0, len(d.Calculated))
	for _, c := range d.Calculated {
		t, ok := calcfield.ParseType(c.DataType)
		if !ok {
			// A metric whose data type no formula can produce is listed by the door, last, and is
			// left out of the plan as the resolver leaves it: a key selected from it is then
			// not in the plan, which Plan.Closure refuses.
			continue
		}
		defs = append(defs, calcfield.Definition{Key: c.Key, Expression: c.Expression, Type: t,
			Min: c.MinValue, Max: c.MaxValue})
	}
	return calcfield.NewPlan(measured, defs)
}

// Metric returns the calculated metric named key.
func (d *CalculatedPlanDoc) Metric(key string) (*CalculatedPlanMetric, bool) {
	for i := range d.Calculated {
		if d.Calculated[i].Key == key {
			return &d.Calculated[i], true
		}
	}
	return nil, false
}
