// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"

	"github.com/devicechain-io/dc-microservice/eventlimit"
)

// Why a leaf of a decoded object was not stored. These are the label values of
// integration_flatten_skipped_total, so they are a closed set.
const (
	SkipString    = "string"
	SkipNull      = "null"
	SkipDepth     = "depth"
	SkipBadKey    = "bad_key"
	SkipDuplicate = "duplicate_key"
	SkipReserved  = "reserved_key"
	SkipNumber    = "bad_number"
)

// SkipReasons lists every reason, so the counter's series can be exported at zero.
var SkipReasons = []string{SkipString, SkipNull, SkipDepth, SkipBadKey, SkipDuplicate, SkipReserved, SkipNumber}

// The link-metric keys an integration with linkMetrics set adds. They belong to the platform: a
// decoded object that names one has that leaf skipped, so a codec cannot spoof the signal.
const (
	KeyRSSI     = "lorawan.rssi"
	KeySNR      = "lorawan.snr"
	KeyGateways = "lorawan.gateways"
)

// measurementKey is the grammar of a stored reading's key. A path that fails it is skipped and
// counted, never rewritten into a different name.
var measurementKey = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)

// ErrTooManyReadings is returned when an uplink flattens to more readings than one event may
// hold. The event is refused whole; nothing is truncated.
type ErrTooManyReadings struct{ Count int }

func (e ErrTooManyReadings) Error() string {
	return fmt.Sprintf("the uplink flattens to %d readings, over the platform limit of %d readings per event; "+
		"nothing was stored (the message is refused whole, not truncated)", e.Count, eventlimit.MaxReadingsPerEvent)
}

// Flattened is what Flatten made of a decoded object.
type Flattened struct {
	// Readings are the keys and values to store, link metrics included.
	Readings map[string]string
	// Leaves is how many readings came from the decoded object itself, link metrics aside. Zero
	// means the uplink carried nothing to read.
	Leaves int
	// Skipped counts the leaves that were not stored, by reason.
	Skipped map[string]int
}

// Flatten turns a decoded object into measurement readings: every numeric or boolean leaf,
// keyed by its dot-joined path (temperature, battery.voltage, sensors.0.value). Numbers keep
// their shortest round-trip form and booleans become true or false. Strings, nulls, values
// nested deeper than MaxDepth and paths that fail the key grammar are skipped, counted by
// reason in Skipped.
//
// It is a pure function of the object: paths are visited in sorted order, so the same object
// always yields the same readings, and a collision of two paths keeps the first in that order.
// Link metrics, when link is not nil, are added under the platform's own keys.
//
// More than eventlimit.MaxReadingsPerEvent readings is an ErrTooManyReadings: the uplink is
// refused whole, never truncated.
func Flatten(object map[string]any, link *Link) (Flattened, error) {
	out := Flattened{Readings: make(map[string]string), Skipped: make(map[string]int)}
	walk(object, "", 0, out.Readings, out.Skipped, link != nil)
	out.Leaves = len(out.Readings)
	if link != nil {
		if link.HasSignal {
			out.Readings[KeyRSSI], _ = formatFloat(link.RSSI)
			out.Readings[KeySNR], _ = formatFloat(link.SNR)
		}
		out.Readings[KeyGateways] = strconv.Itoa(link.Gateways)
	}
	if n := len(out.Readings); n > eventlimit.MaxReadingsPerEvent {
		return Flattened{Skipped: out.Skipped}, ErrTooManyReadings{Count: n}
	}
	return out, nil
}

func walk(node any, path string, depth int, out map[string]string, skipped map[string]int, link bool) {
	if depth > MaxDepth {
		skipped[SkipDepth]++
		return
	}
	switch v := node.(type) {
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			walk(v[k], join(path, k), depth+1, out, skipped, link)
		}
	case []any:
		for i, el := range v {
			walk(el, join(path, strconv.Itoa(i)), depth+1, out, skipped, link)
		}
	case json.Number:
		text, ok := numberText(v)
		if !ok {
			skipped[SkipNumber]++
			return
		}
		put(path, text, out, skipped, link)
	case bool:
		put(path, strconv.FormatBool(v), out, skipped, link)
	case string:
		skipped[SkipString]++
	case nil:
		skipped[SkipNull]++
	default:
		skipped[SkipNumber]++
	}
}

func join(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

func put(path, value string, out map[string]string, skipped map[string]int, link bool) {
	switch {
	case !measurementKey.MatchString(path):
		skipped[SkipBadKey]++
	case link && (path == KeyRSSI || path == KeySNR || path == KeyGateways):
		skipped[SkipReserved]++
	default:
		if _, taken := out[path]; taken {
			skipped[SkipDuplicate]++
			return
		}
		out[path] = value
	}
}
