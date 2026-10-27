// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The recorded bodies under testdata are the network servers' own published examples, with the
// decoded object added where the example has none. They are goldens over every codec, and they
// are what pins the field names the codecs read (see the comment on each codec for what was
// checked upstream).

var receipt = time.Date(2022, 7, 18, 9, 34, 16, 0, time.UTC)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	require.NoError(t, err)
	return b
}

func ptr(s string) *string { return &s }

func chirp(link bool) *Descriptor {
	d := &Descriptor{Token: "cs", Type: ChirpStackV4, Enabled: true, Identity: "DEV_EUI", LinkMetrics: link}
	if err := d.Prepare(); err != nil {
		panic(err)
	}
	return d
}

func tts(identity string, link bool) *Descriptor {
	d := &Descriptor{Token: "ttn", Type: TTSV3, Enabled: true, Identity: identity, LinkMetrics: link}
	if err := d.Prepare(); err != nil {
		panic(err)
	}
	return d
}

func generic(t Type, settings string) *Descriptor {
	d := &Descriptor{Token: "gen", Type: t, Enabled: true, Identity: "ALIAS:custom.pump", Generic: ptr(settings)}
	if err := d.Prepare(); err != nil {
		panic(err)
	}
	return d
}

func req(body []byte, event string) *Request {
	q := url.Values{}
	if event != "" {
		q.Set("event", event)
	}
	return &Request{Body: body, Header: http.Header{}, Query: q, ContentType: "application/json", ReceivedAt: receipt}
}

// The Things Stack's example is dated 2020, so its requests are received just after.
func ttsReq(body []byte, path string) *Request {
	r := req(body, "")
	r.MessageType = path
	r.ReceivedAt = time.Date(2020, 2, 12, 15, 15, 47, 0, time.UTC)
	return r
}

func TestChirpStackUplinkWithADecodedObject(t *testing.T) {
	res, err := Decode(chirp(true), req(fixture(t, "chirpstack/up_decoded.json"), "up"))
	require.NoError(t, err)
	require.Len(t, res.Uplinks, 1)
	u := res.Uplinks[0]
	require.Equal(t, "0101010101010101", u.Identifier)
	require.Equal(t, "lorawan.deveui", u.Scheme)
	require.Equal(t, "cs:3ac7e3c4-4401-4b8d-9386-a5c902f9202d", u.AltId)
	require.True(t, u.SenderTime)
	require.Equal(t, time.Date(2022, 7, 18, 9, 34, 15, 775023242, time.UTC), u.Time)
	require.Equal(t, []byte{0xaa}, u.Frame)
	// The strongest gateway's signal, and the number of gateways that heard it.
	require.Equal(t, &Link{RSSI: -36, SNR: 10.5, Gateways: 2, HasSignal: true}, u.Link)

	flat, err := Flatten(u.Decoded, u.Link)
	require.NoError(t, err)
	require.Equal(t, map[string]string{
		"temperature": "21.5", "humidity": "45", "battery.voltage": "3.31", "battery.low": "false",
		"sensors.0.value": "1.5", "sensors.1.value": "2",
		"lorawan.rssi": "-36", "lorawan.snr": "10.5", "lorawan.gateways": "2",
	}, flat.Readings)
	require.Equal(t, 6, flat.Leaves)
	require.Equal(t, map[string]int{SkipString: 1, SkipNull: 1}, flat.Skipped)
}

func TestChirpStackLinkMetricsAreOffUnlessAsked(t *testing.T) {
	res, err := Decode(chirp(false), req(fixture(t, "chirpstack/up_decoded.json"), "up"))
	require.NoError(t, err)
	require.Nil(t, res.Uplinks[0].Link)
}

func TestChirpStackUplinkWithNoDecodedObject(t *testing.T) {
	res, err := Decode(chirp(false), req(fixture(t, "chirpstack/up_no_object.json"), "up"))
	require.NoError(t, err)
	require.Len(t, res.Uplinks, 1, "the uplink is real, it is just not decoded")
	require.Nil(t, res.Uplinks[0].Decoded)
	require.Equal(t, "aa", res.Uplinks[0].SampleHex(), "the frame is kept to sample")
}

func TestChirpStackEventsThatAreNotUplinksAreIgnored(t *testing.T) {
	for event, file := range map[string]string{"join": "join.json", "status": "status.json"} {
		res, err := Decode(chirp(false), req(fixture(t, "chirpstack/"+file), event))
		require.NoError(t, err, event)
		require.True(t, res.Ignored, event)
		require.Empty(t, res.Uplinks, event)
	}
	// An event type this build has never heard of is also not an uplink.
	res, err := Decode(chirp(false), req([]byte(`{}`), "txack"))
	require.NoError(t, err)
	require.True(t, res.Ignored)
}

func TestChirpStackRefusals(t *testing.T) {
	good := string(fixture(t, "chirpstack/up_decoded.json"))
	swap := func(from, to string) []byte {
		require.Contains(t, good, from)
		return []byte(strings.Replace(good, from, to, 1))
	}
	cases := map[string]struct {
		body  []byte
		event string
		want  string
	}{
		"no event parameter":    {[]byte(good), "", "names no event"},
		"not JSON":              {[]byte("<up/>"), "up", "not valid JSON"},
		"no deduplication id":   {swap(`"deduplicationId": "3ac7e3c4-4401-4b8d-9386-a5c902f9202d",`, ""), "up", "no deduplicationId"},
		"short DevEUI":          {swap(`"0101010101010101"`, `"010101"`), "up", "16 hexadecimal digits"},
		"non-hex DevEUI":        {swap(`"0101010101010101"`, `"01010101010101zz"`), "up", "16 hexadecimal digits"},
		"unreadable time":       {swap(`"2022-07-18T09:34:15.775023242+00:00"`, `"yesterday"`), "up", "RFC 3339"},
		"time over a year old":  {swap(`"2022-07-18T09:34:15.775023242+00:00"`, `"2019-07-18T09:34:15Z"`), "up", "older than"},
		"object that is a list": {swap(`"object": {`, `"object": [{}], "x": {`), "up", "not a JSON object"},
	}
	for name, c := range cases {
		_, err := Decode(chirp(false), req(c.body, c.event))
		require.ErrorIs(t, err, ErrMalformed, name)
		require.Contains(t, err.Error(), c.want, name)
	}
}

// ChirpStack's Protobuf encoding is a setting on the sender's side: it is named, not guessed at.
func TestChirpStackProtobufIsAnUnsupportedEncoding(t *testing.T) {
	r := req(fixture(t, "chirpstack/up_protobuf.bin"), "up")
	r.ContentType = "application/octet-stream"
	_, err := Decode(chirp(false), r)
	require.ErrorIs(t, err, ErrUnsupportedEncoding)
	// Even for an event this build would ignore: the encoding is wrong for all of them.
	r.Query.Set("event", "join")
	_, err = Decode(chirp(false), r)
	require.ErrorIs(t, err, ErrUnsupportedEncoding)
}

func TestTTSUplinkWithADecodedPayload(t *testing.T) {
	res, err := Decode(tts("DEV_EUI", true), ttsReq(fixture(t, "tts/up_decoded.json"), ""))
	require.NoError(t, err)
	require.Len(t, res.Uplinks, 1)
	u := res.Uplinks[0]
	require.Equal(t, "0004A30B001C0530", u.Identifier)
	require.Equal(t, "lorawan.deveui", u.Scheme)
	// device : frame counter : the time the network server received it (the counter alone restarts
	// on a rejoin).
	require.Equal(t, "tts:0004A30B001C0530:1:2020-02-12T15:15:45.789585559Z", u.AltId)
	require.Equal(t, time.Date(2020, 2, 12, 15, 15, 45, 789585559, time.UTC), u.Time)
	require.Equal(t, &Link{RSSI: -35, SNR: 5, Gateways: 2, HasSignal: true}, u.Link)

	flat, err := Flatten(u.Decoded, u.Link)
	require.NoError(t, err)
	require.Equal(t, map[string]string{
		"temperature": "1", "luminosity": "0.64", "motion": "true",
		"lorawan.rssi": "-35", "lorawan.snr": "5", "lorawan.gateways": "2",
	}, flat.Readings)
	require.Equal(t, map[string]int{SkipString: 1}, flat.Skipped)
}

func TestTTSIdentifiesByDeviceIdWhenAsked(t *testing.T) {
	res, err := Decode(tts("DEVICE_ID", false), ttsReq(fixture(t, "tts/up_decoded.json"), ""))
	require.NoError(t, err)
	u := res.Uplinks[0]
	require.Equal(t, "dev1", u.Identifier)
	require.Equal(t, "tts.device-id", u.Scheme)
	require.Equal(t, "tts:0004A30B001C0530:1:2020-02-12T15:15:45.789585559Z", u.AltId, "the dedup key still names the EUI when there is one")
}

func TestTTSUplinkWithNoDecodedPayload(t *testing.T) {
	res, err := Decode(tts("DEV_EUI", false), ttsReq(fixture(t, "tts/up_no_decoded.json"), ""))
	require.NoError(t, err)
	require.Len(t, res.Uplinks, 1)
	require.Nil(t, res.Uplinks[0].Decoded)
	require.Equal(t, "8241de", res.Uplinks[0].SampleHex())
}

func TestTTSMessagesThatAreNotUplinksAreIgnored(t *testing.T) {
	for _, file := range []string{"join_accept.json", "downlink_ack.json"} {
		res, err := Decode(tts("DEV_EUI", false), ttsReq(fixture(t, "tts/"+file), ""))
		require.NoError(t, err, file)
		require.True(t, res.Ignored, file)
	}
	// The webhook posts each message type to a path of its own: another type is ignored without
	// reading the body, and the uplink path is parsed whatever it is spelled.
	for _, path := range []string{"join-accept", "downlink-ack", "location"} {
		r := ttsReq([]byte("not even JSON"), path)
		res, err := Decode(tts("DEV_EUI", false), r)
		require.NoError(t, err, path)
		require.True(t, res.Ignored, path)
	}
	for _, path := range []string{"up", "uplink", "uplinks", "uplink-message", "UPLINK_MESSAGE"} {
		r := ttsReq(fixture(t, "tts/up_decoded.json"), "")
		r.MessageType = path
		res, err := Decode(tts("DEV_EUI", false), r)
		require.NoError(t, err, path)
		require.Len(t, res.Uplinks, 1, path)
	}
}

func TestTTSRefusals(t *testing.T) {
	good := string(fixture(t, "tts/up_decoded.json"))
	swap := func(from, to string) []byte {
		require.Contains(t, good, from)
		return []byte(strings.ReplaceAll(good, from, to))
	}
	cases := map[string]struct {
		body []byte
		want string
	}{
		"not JSON":           {[]byte("nope"), "not valid JSON"},
		"bad DevEUI":         {swap(`"0004A30B001C0530"`, `"0004A30B"`), "16 hexadecimal digits"},
		"no received_at":     {swap(`"received_at"`, `"seen_at"`), "no received_at"},
		"unreadable counter": {swap(`"f_cnt": 1`, `"f_cnt": -1`), "frame counter"},
	}
	for name, c := range cases {
		_, err := Decode(tts("DEV_EUI", false), ttsReq(c.body, ""))
		require.ErrorIs(t, err, ErrMalformed, name)
		require.Contains(t, err.Error(), c.want, name)
	}
	_, err := Decode(tts("DEVICE_ID", false), ttsReq(swap(`"dev1"`, `"Not Valid!"`), ""))
	require.ErrorIs(t, err, ErrMalformed)
}

func TestGenericJSONArraysAreAllOrNothing(t *testing.T) {
	d := generic(GenericJSON, `{"idFrom":"pointer:/sensor","itemsPointer":"/readings","timePointer":"/ts"}`)
	res, err := Decode(d, req(fixture(t, "generic/array.json"), ""))
	require.NoError(t, err)
	require.Len(t, res.Uplinks, 2)
	a, b := res.Uplinks[0], res.Uplinks[1]
	require.Equal(t, []string{"pump-7", "pump-8"}, []string{a.Identifier, b.Identifier})
	require.Equal(t, "custom.pump", a.Scheme)
	require.True(t, a.SenderTime)
	require.Equal(t, time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC), a.Time)
	// The identifier and the time are about the message, not readings.
	require.NotContains(t, a.Decoded, "sensor")
	require.NotContains(t, a.Decoded, "ts")
	flat, err := Flatten(a.Decoded, nil)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"temp": "21.5", "ok": "true"}, flat.Readings)
	require.NotEqual(t, a.AltId, b.AltId)
	require.True(t, strings.HasSuffix(a.AltId, ":2026-10-26T10:00:00Z"), "a sender time makes a retry dedup: %s", a.AltId)

	// One malformed item fails the request, and nothing of it is returned.
	broken := strings.Replace(string(fixture(t, "generic/array.json")), `"sensor": "pump-8"`, `"sensor": [1]`, 1)
	require.Contains(t, broken, `[1]`)
	res, err = Decode(d, req([]byte(broken), ""))
	require.ErrorIs(t, err, ErrMalformed)
	require.Nil(t, res)
}

func TestGenericJSONBareArrayAndCaps(t *testing.T) {
	d := generic(GenericJSON, `{"idFrom":"pointer:/id"}`)
	res, err := Decode(d, req(fixture(t, "generic/bare_array.json"), ""))
	require.NoError(t, err)
	require.Len(t, res.Uplinks, 2)

	items := make([]string, 101)
	for i := range items {
		items[i] = `{"id":"x","v":1}`
	}
	_, err = Decode(d, req([]byte("["+strings.Join(items, ",")+"]"), ""))
	require.ErrorIs(t, err, ErrMalformed)
	require.Contains(t, err.Error(), "nothing was accepted")

	_, err = Decode(d, req([]byte("["+strings.Join(items[:100], ",")+"]"), ""))
	require.NoError(t, err, "exactly the limit is accepted")

	_, err = Decode(d, req([]byte(`[]`), ""))
	require.ErrorIs(t, err, ErrMalformed)
	_, err = Decode(d, req([]byte(`{"id":"x","blob":"`+strings.Repeat("a", MaxDecodedBytes)+`"}`), ""))
	require.ErrorIs(t, err, ErrMalformed)
	require.Contains(t, err.Error(), "over the limit")
}

// A generic sender without a time of its own is stamped at receipt, so a retry carries a new
// stamp and is stored twice. This is the documented caveat, pinned.
func TestGenericRetriesWithoutASenderTimeDoNotDedup(t *testing.T) {
	d := generic(GenericJSON, `{"idFrom":"pointer:/id"}`)
	body := []byte(`{"id":"x","v":1}`)
	r1, r2 := req(body, ""), req(body, "")
	r2.ReceivedAt = receipt.Add(3 * time.Second)
	a, err := Decode(d, r1)
	require.NoError(t, err)
	b, err := Decode(d, r2)
	require.NoError(t, err)
	require.False(t, a.Uplinks[0].SenderTime)
	require.NotEqual(t, a.Uplinks[0].AltId, b.Uplinks[0].AltId)

	// With a sender time the same retry is the same event.
	d = generic(GenericJSON, `{"idFrom":"pointer:/id","timePointer":"/t"}`)
	body = []byte(`{"id":"x","v":1,"t":"2026-10-26T10:00:00Z"}`)
	r2.Body = body
	r1.Body = body
	a, err = Decode(d, r1)
	require.NoError(t, err)
	b, err = Decode(d, r2)
	require.NoError(t, err)
	require.Equal(t, a.Uplinks[0].AltId, b.Uplinks[0].AltId)
}

func TestGenericIdentifierFromAHeaderOrAQueryValue(t *testing.T) {
	d := generic(GenericJSON, `{"idFrom":"header:X-Device"}`)
	r := req([]byte(`{"v":1}`), "")
	r.Header.Set("X-Device", " pump-1 ")
	res, err := Decode(d, r)
	require.NoError(t, err)
	require.Equal(t, "pump-1", res.Uplinks[0].Identifier)
	require.Contains(t, res.Uplinks[0].Decoded, "v")

	_, err = Decode(d, req([]byte(`{"v":1}`), ""))
	require.ErrorIs(t, err, ErrMalformed, "no header, no device")

	d = generic(GenericJSON, `{"idFrom":"query:dev"}`)
	r = req([]byte(`{"v":1}`), "")
	r.Query.Set("dev", "pump-2")
	res, err = Decode(d, r)
	require.NoError(t, err)
	require.Equal(t, "pump-2", res.Uplinks[0].Identifier)
}

func TestGenericBinary(t *testing.T) {
	d := generic(GenericBinary, `{"idFrom":"header:X-Device"}`)
	r := req([]byte{0x01, 0x02, 0xff}, "")
	r.ContentType = "application/octet-stream"
	r.Header.Set("X-Device", "pump-1")
	res, err := Decode(d, r)
	require.NoError(t, err)
	u := res.Uplinks[0]
	require.Equal(t, "pump-1", u.Identifier)
	require.Nil(t, u.Decoded, "nothing decodes a binary frame in this build: it is counted undecoded")
	require.Equal(t, "0102ff", u.SampleHex())
	require.True(t, strings.HasPrefix(u.AltId, "gen:"))
	require.False(t, u.SenderTime)

	long := make([]byte, 2000)
	r.Body = long
	res, err = Decode(d, r)
	require.NoError(t, err)
	require.Len(t, res.Uplinks[0].SampleHex(), 2*MaxSampleBytes, "the sample is cut at 512 bytes")

	r.Body = nil
	_, err = Decode(d, r)
	require.ErrorIs(t, err, ErrMalformed)
}

// The same identifier in either spelling is one device: the codec and the service that stores the
// alias normalize through the same function.
func TestIdentifiersAreNormalizedLikeTheStoredAlias(t *testing.T) {
	body := strings.Replace(string(fixture(t, "chirpstack/up_no_object.json")), `"0101010101010101"`, `"0a0B0c0D0e0F0a0b"`, 1)
	res, err := Decode(chirp(false), req([]byte(body), "up"))
	require.NoError(t, err)
	require.Equal(t, "0A0B0C0D0E0F0A0B", res.Uplinks[0].Identifier)
}

func TestAnUnknownTypeOrBrokenSettingsAreNotParsedAsAGuess(t *testing.T) {
	d := &Descriptor{Token: "x", Type: "MODBUS", Enabled: true, Identity: "DEV_EUI"}
	require.Error(t, d.Prepare())
	_, err := Decode(d, req(nil, ""))
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrMalformed, "a defect is not the sender's mistake")

	g := &Descriptor{Token: "x", Type: GenericJSON, Enabled: true, Identity: "ALIAS:custom.pump"}
	require.Error(t, g.Prepare(), "generic settings are required")
	g = &Descriptor{Token: "x", Type: ChirpStackV4, Enabled: true, Identity: "nonsense"}
	require.Error(t, g.Prepare())
}
