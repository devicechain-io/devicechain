// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"fmt"
	"strings"

	"github.com/devicechain-io/dc-k8s/functionalarea"
)

// relationalAreas are the functional areas whose services open a pool on the shared
// relational store. Every other area uses the instance's event store or no database.
//
// Held against the services' own source by TestTheRelationalAreasAreTheServicesThatOpenTheRelationalStore,
// so an area that starts or stops using the store cannot leave its instance's budget
// wrong without failing the build.
var relationalAreas = map[functionalarea.FunctionalArea]bool{
	functionalarea.UserManagement:   true,
	functionalarea.DeviceManagement: true,
	functionalarea.DeviceState:      true,
	functionalarea.DashboardMgmt:    true,
	functionalarea.CommandDelivery:  true,
	functionalarea.NotificationMgmt: true,
	functionalarea.EventProcessing:  true,
	functionalarea.OutboundConn:     true,
	functionalarea.AiInference:      true,
	functionalarea.UpdateMgmt:       true,
}

const (
	// servicePoolSize mirrors defaultMaxOpenConnections in backend/core/rdb, which no
	// service overrides; held to it by the same test.
	servicePoolSize = 20
	// rolloutSurge sizes the limit for a rollout rather than the steady state: with
	// maxSurge 1 a new pod holds a full pool before the old one lets go of its own.
	rolloutSurge = 2
)

// instanceAreas is the set of functional areas an instance runs: the explicit set when
// --enable-area expanded one, otherwise the profile's. The one reading of it for the
// sizing that depends on it (instanceConnectionLimit, persistenceFor).
func instanceAreas(st *State) ([]functionalarea.FunctionalArea, error) {
	areas := make([]functionalarea.FunctionalArea, 0, len(st.EnabledAreas))
	for _, a := range st.EnabledAreas {
		areas = append(areas, functionalarea.FunctionalArea(a))
	}
	if len(areas) > 0 {
		return areas, nil
	}
	return functionalarea.ResolveEnabled(st.Profile, nil)
}

// instanceConnectionLimit is how many connections an instance's login may hold on the
// shared relational store: every pod of every relational area it runs, at a full pool,
// mid-rollout.
//
// 🔴 THE OPERATOR'S OVERLAY IS PART OF THE SIZING, NOT A FOOTNOTE TO IT. An overlay may raise
// an area's replicas (its own, or the top-level count every area without one inherits) and
// its pool (config.rdbConfiguration.maxOpenConnections). Sized from dcctl's topology alone,
// the login's CONNECTION LIMIT would then be below what the pods open: the store keeps
// reporting healthy, pools open lazily, and the failure is "too many connections for role"
// under load, long after the run that caused it went green. Read from the overlay, a raise
// goes through the same admission as everything else, so the store's budget refuses it
// before anything is written.
func instanceConnectionLimit(st *State) (int, error) {
	areas, err := instanceAreas(st)
	if err != nil {
		return 0, err
	}
	var chartDefaults map[string]interface{}
	if len(st.ValuesOverlay) > 0 {
		ch, err := loadEmbeddedChart()
		if err != nil {
			return 0, fmt.Errorf("loading the chart to size the values overlay's connections: %w", err)
		}
		chartDefaults = ch.Values
	}
	n := 0
	for _, a := range areas {
		if !relationalAreas[a] {
			continue
		}
		replicas, pool := 1, servicePoolSize
		if len(st.ValuesOverlay) > 0 {
			replicas = overlayAreaReplicas(chartDefaults, st.ValuesOverlay, string(a))
			pool = overlayAreaPool(st.ValuesOverlay, string(a))
		}
		n += replicas * pool * rolloutSurge
	}
	return n, nil
}

// overlayAreaReplicas is the replica count the chart renders for area with the overlay over
// its defaults, mirroring deployment.yaml's `get $areaCfg "replicas" | default
// .Values.replicas`: the area's own count unless it is unset or zero, then the top-level one.
// Never below one: a pod count of zero still has to be admitted for the next rollout.
func overlayAreaReplicas(chartDefaults, overlay map[string]interface{}, area string) int {
	pick := func(path ...string) (int, bool) {
		if v, ok := valueAt(overlay, path...); ok {
			return asCount(v)
		}
		return asCount(valueAt2(chartDefaults, path...))
	}
	if r, ok := pick("functionalAreas", area, "replicas"); ok && r > 0 {
		return r
	}
	if r, ok := pick("replicas"); ok && r > 0 {
		return r
	}
	return 1
}

// overlayAreaPool is the pool an area's pods open on the relational store with the overlay's
// config.rdbConfiguration.maxOpenConnections, or the services' default when it sets none.
// The services bind their configuration as encoding/json does, case-insensitively, so every
// spelling is read and the largest wins.
func overlayAreaPool(overlay map[string]interface{}, area string) int {
	pool := 0
	cfg, _ := valueAt(overlay, "functionalAreas", area, "config")
	for k, v := range asMap(cfg) {
		if !strings.EqualFold(k, "rdbConfiguration") {
			continue
		}
		for k2, v2 := range asMap(v) {
			if strings.EqualFold(k2, "maxOpenConnections") {
				if n, ok := asCount(v2); ok && n > pool {
					pool = n
				}
			}
		}
	}
	if pool <= 0 {
		return servicePoolSize
	}
	return pool
}

// overlayBudgetHint is appended to a budget refusal when the operator's overlay is part of
// the need, so the refusal names the lever the operator just pulled.
func overlayBudgetHint(st *State) string {
	if len(st.ValuesOverlay) == 0 {
		return ""
	}
	return ". The values overlay's replica counts and connection pools (config.rdbConfiguration." +
		"maxOpenConnections) are part of that need: lower them in the overlay, or remove it with --reset-values"
}

func asMap(v interface{}) map[string]interface{} {
	m, _ := v.(map[string]interface{})
	return m
}

// valueAt walks a values tree; ok reports the key was PRESENT (a null is present).
func valueAt(m map[string]interface{}, path ...string) (interface{}, bool) {
	var cur interface{} = m
	for _, seg := range path {
		mm, isMap := cur.(map[string]interface{})
		if !isMap {
			return nil, false
		}
		if cur, isMap = mm[seg]; !isMap {
			return nil, false
		}
	}
	return cur, true
}

func valueAt2(m map[string]interface{}, path ...string) interface{} {
	v, _ := valueAt(m, path...)
	return v
}

// asCount reads a whole number out of a decoded values document (YAML and JSON both decode
// numbers as float64).
func asCount(v interface{}) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case int64:
		return int(n), true
	}
	return 0, false
}
