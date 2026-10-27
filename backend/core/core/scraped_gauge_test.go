// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// A scraped gauge is read at every scrape, so a value that moves with the clock is current.
func TestScrapedGaugeIsReadAtEveryScrape(t *testing.T) {
	ms := &Microservice{FunctionalArea: "scrape-test"}
	reg := prometheus.NewRegistry()
	ms.UseMetricsRegistry(reg)

	state := 0.0
	ms.NewScrapedGaugeVec("breaker_state", "state", []string{"tenant"}, func() []GaugeSample {
		return []GaugeSample{{Labels: []string{"acme"}, Value: state}}
	})

	want := func(v string) string {
		return "# HELP devicechain_scrapetest_breaker_state state\n# TYPE devicechain_scrapetest_breaker_state gauge\n" +
			"devicechain_scrapetest_breaker_state{tenant=\"acme\"} " + v + "\n"
	}
	if err := testutil.GatherAndCompare(reg, strings.NewReader(want("0"))); err != nil {
		t.Fatal(err)
	}
	state = 2 // nothing touched the gauge; the next scrape still sees it
	if err := testutil.GatherAndCompare(reg, strings.NewReader(want("2"))); err != nil {
		t.Fatal(err)
	}
}

func TestScrapedGaugeWithNoRegistryIsANoOp(t *testing.T) {
	(&Microservice{FunctionalArea: "x"}).NewScrapedGaugeVec("g", "h", nil, func() []GaugeSample { return nil })
}
