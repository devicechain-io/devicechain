// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package integrationcodec

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// A decoder's fPort section and its frame are only as good as what the codec read: these pin that
// every LoRaWAN codec carries the port and counter, and that no codec decides what a bad frame
// encoding means.

func TestChirpStackCarriesFPortAndFCnt(t *testing.T) {
	res, err := Decode(chirp(false), req(fixture(t, "chirpstack/up_decoded.json"), "up"))
	require.NoError(t, err)
	u := res.Uplinks[0]
	require.Equal(t, 1, u.FPort)
	require.Equal(t, int64(10), u.FCnt)
	require.Equal(t, "qg==", u.FrameText)
	require.Equal(t, EncodingBase64, u.FrameEncoding)
	require.NoError(t, u.FrameErr)
}

func TestChirpStackOmittedFPortAndFCntAreTheProtobufDefault(t *testing.T) {
	body := strings.NewReplacer(`"fCnt": 10,`, "", `"fPort": 1,`, "").Replace(string(fixture(t, "chirpstack/up_decoded.json")))
	res, err := Decode(chirp(false), req([]byte(body), "up"))
	require.NoError(t, err)
	require.Equal(t, 0, res.Uplinks[0].FPort, "absent is 0 on a LoRaWAN envelope, not the NoLoRaWAN marker")
	require.Equal(t, int64(0), res.Uplinks[0].FCnt)
}

func TestChirpStackRefusesAPortOrCounterThatIsNotOne(t *testing.T) {
	good := string(fixture(t, "chirpstack/up_decoded.json"))
	for name, body := range map[string]string{
		"port over 255":    strings.Replace(good, `"fPort": 1,`, `"fPort": 256,`, 1),
		"negative port":    strings.Replace(good, `"fPort": 1,`, `"fPort": -1,`, 1),
		"fractional port":  strings.Replace(good, `"fPort": 1,`, `"fPort": 1.5,`, 1),
		"counter over u32": strings.Replace(good, `"fCnt": 10,`, `"fCnt": 4294967296,`, 1),
	} {
		_, err := Decode(chirp(false), req([]byte(body), "up"))
		require.ErrorIs(t, err, ErrMalformed, name)
	}
}

func TestTTSCarriesFPortAndFCnt(t *testing.T) {
	res, err := Decode(tts("DEV_EUI", false), ttsReq(fixture(t, "tts/up_decoded.json"), ""))
	require.NoError(t, err)
	u := res.Uplinks[0]
	require.Equal(t, 1, u.FPort)
	require.Equal(t, int64(1), u.FCnt)
	require.Equal(t, "gkHe", u.FrameText)
	require.Equal(t, []byte{0x82, 0x41, 0xde}, u.Frame)
}

func TestTTSRefusesAPortThatIsNotOne(t *testing.T) {
	good := string(fixture(t, "tts/up_decoded.json"))
	_, err := Decode(tts("DEV_EUI", false), ttsReq([]byte(strings.Replace(good, `"f_port": 1,`, `"f_port": 300,`, 1)), ""))
	require.ErrorIs(t, err, ErrMalformed)
}

func TestGenericSendersCarryNoLoRaWANPortOrCounter(t *testing.T) {
	res, err := Decode(generic(GenericJSON, `{"idFrom":"pointer:/id"}`), req([]byte(`{"id":"pump-1","v":1}`), ""))
	require.NoError(t, err)
	require.Equal(t, NoLoRaWAN, res.Uplinks[0].FPort)
	require.Equal(t, int64(NoLoRaWAN), res.Uplinks[0].FCnt)

	d := generic(GenericBinary, `{"idFrom":"header:X-Device"}`)
	r := req([]byte{1}, "")
	r.Header.Set("X-Device", "pump-1")
	res, err = Decode(d, r)
	require.NoError(t, err)
	require.Equal(t, NoLoRaWAN, res.Uplinks[0].FPort)
	require.Equal(t, int64(NoLoRaWAN), res.Uplinks[0].FCnt)
}

// A frame that does not decode is no longer swallowed by the codec: the text and the error are
// kept for the route to judge once it knows whether a decoder needs the frame. The request itself
// still decodes, which is what keeps Cut A's behaviour for an integration with no decoder.
func TestABadFrameEncodingIsRetainedNotDecidedByTheCodec(t *testing.T) {
	good := string(fixture(t, "chirpstack/up_decoded.json"))
	res, err := Decode(chirp(false), req([]byte(strings.Replace(good, `"data": "qg=="`, `"data": "not base64!"`, 1)), "up"))
	require.NoError(t, err, "the codec does not refuse")
	u := res.Uplinks[0]
	require.Nil(t, u.Frame)
	require.Equal(t, "not base64!", u.FrameText)
	require.Equal(t, EncodingBase64, u.FrameEncoding)
	require.Error(t, u.FrameErr)
	require.NotNil(t, u.Decoded, "the decoded object is untouched")

	ttsBad := strings.Replace(string(fixture(t, "tts/up_decoded.json")), `"gkHe"`, `"@@@@"`, 1)
	res, err = Decode(tts("DEV_EUI", false), ttsReq([]byte(ttsBad), ""))
	require.NoError(t, err)
	require.Error(t, res.Uplinks[0].FrameErr)
	require.Equal(t, "@@@@", res.Uplinks[0].FrameText)

	d := generic(GenericJSON, `{"idFrom":"pointer:/id","framePointer":"/raw","frameEncoding":"hex"}`)
	res, err = Decode(d, req([]byte(`{"id":"pump-1","raw":"0aFF"}`), ""))
	require.NoError(t, err)
	require.Equal(t, []byte{0x0a, 0xff}, res.Uplinks[0].Frame)
	require.Equal(t, EncodingHex, res.Uplinks[0].FrameEncoding)
	require.NoError(t, res.Uplinks[0].FrameErr)

	res, err = Decode(d, req([]byte(`{"id":"pump-1","raw":"0aF"}`), ""))
	require.NoError(t, err)
	require.Nil(t, res.Uplinks[0].Frame)
	require.Equal(t, "0aF", res.Uplinks[0].FrameText)
	require.Error(t, res.Uplinks[0].FrameErr)
}

func TestARawBinaryBodyHasNoFrameText(t *testing.T) {
	d := generic(GenericBinary, `{"idFrom":"header:X-Device"}`)
	r := req([]byte{1, 2}, "")
	r.Header.Set("X-Device", "pump-1")
	res, err := Decode(d, r)
	require.NoError(t, err)
	u := res.Uplinks[0]
	require.Equal(t, []byte{1, 2}, u.Frame)
	require.Empty(t, u.FrameText)
	require.NoError(t, u.FrameErr)
}

// nested builds {"a":{"a":...}} with n containers.
func nested(n int) any {
	var v any = float64(1)
	for i := 0; i < n; i++ {
		v = map[string]any{"a": v}
	}
	return v
}

func TestCheckDepthRefusesPastTheLimitAndAdmitsTheLimit(t *testing.T) {
	require.NoError(t, CheckDepth(nested(MaxDepth)), "exactly MaxDepth containers is admitted")
	err := CheckDepth(nested(MaxDepth + 1))
	require.ErrorIs(t, err, ErrTooDeep)
	require.ErrorIs(t, err, ErrMalformed, "the route answers it 400")

	// Arrays count too, and so does a deep branch beside a shallow one.
	var arr any = float64(1)
	for i := 0; i < MaxDepth+1; i++ {
		arr = []any{arr}
	}
	require.ErrorIs(t, CheckDepth(map[string]any{"shallow": 1, "deep": arr}), ErrTooDeep)
	require.NoError(t, CheckDepth(map[string]any{"shallow": 1, "list": []any{1, 2}}))
	require.NoError(t, CheckDepth(nil))
}

// The depth limit is the one Flatten applies: a value CheckDepth admits never has a leaf
// skipped for depth, and one it refuses always would.
func TestCheckDepthAgreesWithFlattensDepthCutoff(t *testing.T) {
	for _, n := range []int{MaxDepth, MaxDepth + 1} {
		flat, err := Flatten(nested(n).(map[string]any), nil)
		require.NoError(t, err)
		skipped := flat.Skipped[SkipDepth] > 0
		require.Equal(t, CheckDepth(nested(n)) != nil, skipped, "depth %d", n)
	}
}

// chain is {"a":{"a":...1...}} with n containers.
func chain(n int) string {
	return strings.Repeat(`{"a":`, n) + "1" + strings.Repeat("}", n)
}

// The depth walk runs in the codec on every uplink, and its verdict rides the Uplink for the
// route to act on: Decode itself does not refuse, because Cut A flattens a deep object and counts
// what it skipped.
func TestEveryCodecRecordsHowDeepTheDecodedObjectIs(t *testing.T) {
	cs := string(fixture(t, "chirpstack/up_decoded.json"))
	i := strings.Index(cs, `"object"`)
	require.Greater(t, i, 0)
	tts0 := string(fixture(t, "tts/up_decoded.json"))
	j := strings.Index(tts0, `"decoded_payload"`)
	require.Greater(t, j, 0)

	for _, depth := range []int{MaxDepth, MaxDepth + 1} {
		wantErr := depth > MaxDepth

		// An envelope with the deep object substituted: replace everything from the key to the
		// end of its (balanced) value.
		csBody := replaceValue(t, cs, `"object"`, chain(depth))
		res, err := Decode(chirp(false), req([]byte(csBody), "up"))
		require.NoError(t, err, "the codec does not refuse a deep object: Cut A flattens it")
		require.Equal(t, wantErr, res.Uplinks[0].DepthErr != nil, "chirpstack depth %d", depth)

		ttsBody := replaceValue(t, tts0, `"decoded_payload"`, chain(depth))
		res, err = Decode(tts("DEV_EUI", false), ttsReq([]byte(ttsBody), ""))
		require.NoError(t, err)
		require.Equal(t, wantErr, res.Uplinks[0].DepthErr != nil, "tts depth %d", depth)

		// The generic item is itself one container, so the chain below it is one shorter.
		res, err = Decode(generic(GenericJSON, `{"idFrom":"pointer:/id"}`),
			req([]byte(`{"id":"pump-1","x":`+chain(depth-1)+`}`), ""))
		require.NoError(t, err)
		require.Equal(t, wantErr, res.Uplinks[0].DepthErr != nil, "generic depth %d", depth)
		if wantErr {
			require.ErrorIs(t, res.Uplinks[0].DepthErr, ErrTooDeep)
			require.ErrorIs(t, res.Uplinks[0].DepthErr, ErrMalformed)
		}
	}
}

// replaceValue swaps the JSON value after key (an object value, balanced) for repl.
func replaceValue(t *testing.T, doc, key, repl string) string {
	t.Helper()
	i := strings.Index(doc, key)
	require.GreaterOrEqual(t, i, 0)
	k := strings.Index(doc[i:], "{")
	require.GreaterOrEqual(t, k, 0)
	start, depth := i+k, 0
	for end := start; end < len(doc); end++ {
		switch doc[end] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return doc[:start] + repl + doc[end+1:]
			}
		}
	}
	t.Fatal("unbalanced fixture")
	return ""
}
