// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/eventlimit"
	"github.com/stretchr/testify/require"
)

func parse(t *testing.T, doc string) map[string]any {
	t.Helper()
	var obj map[string]any
	dec := json.NewDecoder(bytes.NewReader([]byte(doc)))
	dec.UseNumber()
	require.NoError(t, dec.Decode(&obj))
	return obj
}

func TestFlattenLeafRules(t *testing.T) {
	flat, err := Flatten(parse(t, `{
		"a": 1.50, "b": 2.0, "c": 3, "d": 1e3, "e": 1.5e-7, "f": 12345678901234567890,
		"g": true, "h": false, "s": "text", "n": null,
		"nest": {"x": 1, "y": {"z": 2}},
		"list": [10, {"v": 11}, "str"],
		"bad key": 5, "ok_key-1": 6, "p.q": 7, "p": {"q": 8}
	}`), nil)
	require.NoError(t, err)
	require.Equal(t, map[string]string{
		"a": "1.5", "b": "2", "c": "3", "d": "1000", "e": "1.5e-07", "f": "12345678901234567890",
		"g": "true", "h": "false",
		"nest.x": "1", "nest.y.z": "2",
		"list.0": "10", "list.1.v": "11",
		"ok_key-1": "6", "p.q": "8",
	}, flat.Readings)
	// "p.q" is both a key and a path: the sorted order decides, and the loser is counted, never silently dropped.
	require.Equal(t, map[string]int{SkipString: 2, SkipNull: 1, SkipBadKey: 1, SkipDuplicate: 1}, flat.Skipped)
	require.Equal(t, len(flat.Readings), flat.Leaves)
}

func TestFlattenIsDeterministic(t *testing.T) {
	doc := `{"z":1,"y":{"x":2,"w":[3,4]},"a.b":5,"a":{"b":6},"m":{"n":{"o":7}}}`
	first, err := Flatten(parse(t, doc), nil)
	require.NoError(t, err)
	for i := 0; i < 50; i++ {
		again, err := Flatten(parse(t, doc), nil)
		require.NoError(t, err)
		require.Equal(t, first, again, "map iteration order must not reach the output")
	}
}

func TestFlattenSkipsAndCountsDeepValues(t *testing.T) {
	deep := strings.Repeat(`{"k":`, MaxDepth+2) + `1` + strings.Repeat(`}`, MaxDepth+2)
	flat, err := Flatten(parse(t, `{"top": 1, "deep": `+deep+`}`), nil)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"top": "1"}, flat.Readings)
	require.Equal(t, 1, flat.Skipped[SkipDepth])
}

// An uplink over the per-event limit is refused whole. A flatten that truncated would store a
// prefix of the readings and report success.
func TestFlattenRefusesMoreThanTheLimitWholeAndNeverTruncates(t *testing.T) {
	obj := map[string]any{}
	for i := 0; i < eventlimit.MaxReadingsPerEvent; i++ {
		obj[fmt.Sprintf("k%03d", i)] = json.Number("1")
	}
	flat, err := Flatten(obj, nil)
	require.NoError(t, err, "exactly the limit is stored")
	require.Len(t, flat.Readings, eventlimit.MaxReadingsPerEvent)

	obj["one-more"] = json.Number("1")
	flat, err = Flatten(obj, nil)
	var tooMany ErrTooManyReadings
	require.ErrorAs(t, err, &tooMany)
	require.Equal(t, eventlimit.MaxReadingsPerEvent+1, tooMany.Count)
	require.Nil(t, flat.Readings, "nothing partial comes back")

	// The link metrics count against the same limit.
	delete(obj, "one-more")
	_, err = Flatten(obj, &Link{RSSI: -1, SNR: 1, Gateways: 1, HasSignal: true})
	require.ErrorAs(t, err, &tooMany)
}

func TestFlattenLinkMetricsAreThePlatformsOwn(t *testing.T) {
	flat, err := Flatten(parse(t, `{"lorawan.rssi": 999, "lorawan.snr": 999, "lorawan.gateways": 999, "t": 1}`),
		&Link{RSSI: -71.5, SNR: 8, Gateways: 3, HasSignal: true})
	require.NoError(t, err)
	require.Equal(t, map[string]string{"t": "1", "lorawan.rssi": "-71.5", "lorawan.snr": "8", "lorawan.gateways": "3"}, flat.Readings)
	require.Equal(t, 3, flat.Skipped[SkipReserved], "a codec cannot spoof the link")
	require.Equal(t, 1, flat.Leaves)

	// Without link metrics the same keys are the sender's.
	flat, err = Flatten(parse(t, `{"lorawan.rssi": 999}`), nil)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"lorawan.rssi": "999"}, flat.Readings)

	// A gateway count with no signal (no gateway reported both) is still reported.
	flat, err = Flatten(parse(t, `{"t": 1}`), &Link{Gateways: 2})
	require.NoError(t, err)
	require.Equal(t, map[string]string{"t": "1", "lorawan.gateways": "2"}, flat.Readings)
}

func TestFlattenOfNothingHasNoLeaves(t *testing.T) {
	flat, err := Flatten(parse(t, `{"s":"x","n":null}`), &Link{Gateways: 1})
	require.NoError(t, err)
	require.Zero(t, flat.Leaves, "link metrics alone are not a decoded uplink")
}
