// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/config"
)

// The chart states no design rate (raw helm leaves the streams sized by their byte ceilings),
// and dcctl states one per profile: the standard rate, or the compact preset's.
func TestTheDesignRateIsStatedByDcctlAndNotByTheChart(t *testing.T) {
	t.Parallel()
	if got := renderInstanceConfig(t, nil).Infrastructure.Nats.StreamDesignRate; got != 0 {
		t.Errorf("the chart alone states a design rate of %d: a raw helm upgrade would raise a hand-sized instance's reservation", got)
	}
	std := renderFromState(t, compactState(false)).Infrastructure.Nats
	if std.StreamDesignRate != config.DefaultStreamDesignRate {
		t.Errorf("the standard profile's design rate is %d, want %d", std.StreamDesignRate, config.DefaultStreamDesignRate)
	}
	cmp := renderFromState(t, compactState(true)).Infrastructure.Nats
	if cmp.StreamDesignRate != compact.StreamDesignRate {
		t.Errorf("the compact profile's design rate is %d, want %d", cmp.StreamDesignRate, compact.StreamDesignRate)
	}
}

// An operator reaches the runway through --values, and the schema holds the keys to range.
func TestTheRunwayKeysAreTunableAndRangeChecked(t *testing.T) {
	ok := map[string]interface{}{"instance": map[string]interface{}{"config": map[string]interface{}{
		"infrastructure": map[string]interface{}{"nats": map[string]interface{}{
			"streamDesignRate": 20000, "streamRunwayMinutes": 15, "streamMaxBytes": 2 << 30,
			"streamMaxBytesCold": 128 << 20, "streamMaxMsgs": 9000000}}}}}
	if err := ValidateValuesOverlay(ok); err != nil {
		t.Fatalf("a runway overlay was refused: %v", err)
	}
	for name, bad := range map[string]int{"streamDesignRate": -1, "streamRunwayMinutes": 10081} {
		o := map[string]interface{}{"instance": map[string]interface{}{"config": map[string]interface{}{
			"infrastructure": map[string]interface{}{"nats": map[string]interface{}{name: bad}}}}}
		if err := ValidateValuesOverlay(o); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s=%d was not refused by name: %v", name, bad, err)
		}
	}
	// What is not a capacity dial stays dcctl's.
	o := map[string]interface{}{"instance": map[string]interface{}{"config": map[string]interface{}{
		"infrastructure": map[string]interface{}{"nats": map[string]interface{}{"hostname": "elsewhere"}}}}}
	if err := ValidateValuesOverlay(o); err == nil {
		t.Error("an overlay set the broker hostname")
	}
}

// The overlay really lands in the authored document the reservation precheck reads.
func TestAnOverlayRunwayReachesTheAuthoredDocument(t *testing.T) {
	st := compactState(false)
	st.ValuesOverlay = map[string]interface{}{"instance": map[string]interface{}{"config": map[string]interface{}{
		"infrastructure": map[string]interface{}{"nats": map[string]interface{}{"streamDesignRate": 20000}}}}}
	ch, err := loadEmbeddedChart()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := renderInstanceConfigFromChart(t.Context(), ch, authoringValues(st))
	if err != nil || cfg == nil {
		t.Fatalf("rendering the authored document: %v", err)
	}
	if n := cfg.Infrastructure.Nats; n.StreamDesignRate != 20000 {
		t.Errorf("the overlay's design rate did not replace the profile's: %d", cfg.Infrastructure.Nats.StreamDesignRate)
	}
}
