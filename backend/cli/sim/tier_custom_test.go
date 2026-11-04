// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import "testing"

func TestCustomTierParsesIntegerFloors(t *testing.T) {
	spec, err := CustomTier("rc-100k", `{"ingestReadingsPerSecond":100000,"ingestBurst":200000}`)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Token != "rc-100k" || spec.Config["ingestReadingsPerSecond"] != 100000 || spec.Config["ingestBurst"] != 200000 {
		t.Fatalf("unexpected spec: %+v", spec)
	}
}

func TestCustomTierRefusesWhatIsNotAnObjectOfIntegers(t *testing.T) {
	for name, raw := range map[string]string{
		"empty object": `{}`,
		"not json":     `ingest=1`,
		"string value": `{"ingestBurst":"lots"}`,
		"float value":  `{"ingestBurst":1.5}`,
		"array":        `[1]`,
	} {
		if _, err := CustomTier("rc-100k", raw); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}
