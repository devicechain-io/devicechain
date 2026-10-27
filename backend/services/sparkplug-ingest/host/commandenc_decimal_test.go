// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package host

import (
	"encoding/json"
	"math/big"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var intFamilies = map[string]uint32{
	"Int8": datatypeInt8, "Int16": datatypeInt16, "Int32": datatypeInt32, "Int64": datatypeInt64,
	"UInt8": datatypeUInt8, "UInt16": datatypeUInt16, "UInt32": datatypeUInt32, "UInt64": datatypeUInt64,
	"DateTime": datatypeDateTime,
}

func encodeInt(dt uint32, s string) error {
	_, err := EncodeMetricValue(dt, json.Number(s))
	return err
}

// A decimal or exponent number is judged exactly. It used to go through a float64, which rounds
// 1.0000000000000001 to 1 and underflows 1e-400 to 0, so a non-integral request was sent as an
// integer actuator value the operator did not ask for.
func TestNonIntegralJSONIsRefusedForEveryIntegerFamily(t *testing.T) {
	for name, dt := range intFamilies {
		for _, s := range []string{
			"1.0000000000000001", "127.000000000000001", "1e-400", "0.5", "1.5", "1e-1", "-0.0000001",
			"7.0000000000000000000000000001", "2.5e0",
		} {
			assert.Error(t, encodeInt(dt, s), "%s accepted non-integral %s", name, s)
		}
	}
}

func TestExactWholeDecimalsAreAcceptedForEveryIntegerFamily(t *testing.T) {
	for name, dt := range intFamilies {
		for _, s := range []string{"7.0", "7.000", "70e-1", "0.0", "-0.0"} {
			assert.NoError(t, encodeInt(dt, s), "%s refused whole %s", name, s)
		}
		// 1e3 is whole, and in range for everything above 8 bits.
		if intRanges[dt].max.Cmp(big.NewInt(1000)) >= 0 {
			assert.NoError(t, encodeInt(dt, "1e3"), "%s refused 1e3", name)
			assert.NoError(t, encodeInt(dt, "1E+3"), "%s refused 1E+3", name)
		} else {
			assert.Error(t, encodeInt(dt, "1e3"), "%s accepted 1e3 beyond its range", name)
		}
	}
	for _, s := range []string{"-1.0", "-1e0"} {
		assert.NoError(t, encodeInt(datatypeInt8, s))
		assert.Error(t, encodeInt(datatypeUInt8, s), "an unsigned type accepted %s", s)
	}
}

func TestIntegerBoundariesStayExactInEveryForm(t *testing.T) {
	for name, dt := range intFamilies {
		r := intRanges[dt]
		for _, v := range []*big.Int{r.min, r.max} {
			digits := v.String()
			for _, s := range []string{digits, digits + ".0", digits + ".000"} {
				n, err := exactInteger(dt, json.Number(s), false)
				require.NoError(t, err, "%s refused its boundary %s", name, s)
				assert.Zero(t, n.Cmp(v), "%s: %s came back as %s", name, s, n)
			}
			// One past the boundary is refused, as a plain integer and as a decimal.
			past := new(big.Int)
			if v.Cmp(r.max) == 0 {
				past.Add(v, big.NewInt(1))
			} else {
				past.Sub(v, big.NewInt(1))
			}
			assert.Error(t, encodeInt(dt, past.String()), "%s accepted %s", name, past)
			assert.Error(t, encodeInt(dt, past.String()+".0"), "%s accepted %s.0", name, past)
		}
	}
	// The whole 64-bit range as a scientific number, and as a string of digits.
	n, err := exactInteger(datatypeUInt64, json.Number("1.8446744073709551615e19"), true)
	require.NoError(t, err)
	assert.Equal(t, "18446744073709551615", n.String())
	n, err = exactInteger(datatypeInt64, "-9223372036854775808", true)
	require.NoError(t, err)
	assert.Equal(t, "-9223372036854775808", n.String())
}

// The expansion of an exponent is bounded, and anything that is not a JSON number is refused.
func TestHugeAndMalformedNumbersAreRefusedWithoutExpanding(t *testing.T) {
	for _, s := range []string{
		"1e999999999", "1e-999999999", "1e1001", "9e99999", strings.Repeat("9", 200), "1/3", "0x10", "1_000",
		"+5", ".5", "5.", "NaN", "Infinity", "1e", "",
	} {
		assert.Error(t, encodeInt(datatypeInt64, s), "accepted %q", s)
	}
}

// The float datatypes and float callers are untouched.
func TestFloatPathsStillWork(t *testing.T) {
	assert.NoError(t, encodeInt(datatypeDouble, "1.0000000000000001"))
	assert.NoError(t, encodeInt(datatypeFloat, "1.5"))
	_, err := EncodeMetricValue(datatypeInt32, 7.0)
	assert.NoError(t, err)
	_, err = EncodeMetricValue(datatypeInt32, 7.5)
	assert.Error(t, err)
}
