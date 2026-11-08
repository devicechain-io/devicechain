// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package lpwan

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type fakeQuerier struct {
	calls  int
	body   string
	err    error
	tenant string
	url    string
	vars   map[string]any
	query  string
}

func (q *fakeQuerier) Query(_ context.Context, baseURL, tenant, query string, vars map[string]any, out any) error {
	q.calls++
	q.url, q.tenant, q.query, q.vars = baseURL, tenant, query, vars
	if q.err != nil {
		return q.err
	}
	return json.Unmarshal([]byte(q.body), out)
}

func TestServiceDevicesReadsTheDevEUIAlias(t *testing.T) {
	q := &fakeQuerier{body: `{"devicesByToken":[{"token":"dev-1","aliases":[
		{"scheme":"tts.device-id","value":"x"},{"scheme":"lorawan.deveui","value":"0a0b0c0d0e0f1011"}]}]}`}
	d := NewServiceDevices(q, "http://dm/graphql")
	eui, err := d.DevEUI(context.Background(), "acme", "dev-1")
	require.NoError(t, err)
	require.Equal(t, "0A0B0C0D0E0F1011", eui)
	require.Equal(t, "acme", q.tenant)
	require.Equal(t, "http://dm/graphql", q.url)
	require.Equal(t, map[string]any{"tokens": []string{"dev-1"}}, q.vars)
	require.Contains(t, q.query, "devicesByToken")
}

func TestServiceDevicesAbsences(t *testing.T) {
	for name, body := range map[string]string{
		"no device":        `{"devicesByToken":[]}`,
		"other device":     `{"devicesByToken":[{"token":"someone-else","aliases":[{"scheme":"lorawan.deveui","value":"0102030405060708"}]}]}`,
		"aliases withheld": `{"devicesByToken":[{"token":"dev-1","aliases":null}]}`,
		"no deveui alias":  `{"devicesByToken":[{"token":"dev-1","aliases":[{"scheme":"tts.device-id","value":"x"}]}]}`,
		"malformed deveui": `{"devicesByToken":[{"token":"dev-1","aliases":[{"scheme":"lorawan.deveui","value":"nothex"}]}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := NewServiceDevices(&fakeQuerier{body: body}, "u").DevEUI(context.Background(), "acme", "dev-1")
			require.ErrorIs(t, err, ErrNoDevEUI)
		})
	}
}

func TestServiceDevicesTransientErrorIsNotAnAbsence(t *testing.T) {
	_, err := NewServiceDevices(&fakeQuerier{err: errors.New("down")}, "u").DevEUI(context.Background(), "acme", "dev-1")
	require.Error(t, err)
	require.False(t, errors.Is(err, ErrNoDevEUI))
}

func TestServiceDevicesCacheIsPerTenantAndOnlyForFoundValues(t *testing.T) {
	q := &fakeQuerier{body: `{"devicesByToken":[{"token":"dev-1","aliases":[{"scheme":"lorawan.deveui","value":"0102030405060708"}]}]}`}
	d := NewServiceDevices(q, "u")
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	d.now = func() time.Time { return now }

	_, _ = d.DevEUI(context.Background(), "acme", "dev-1")
	_, _ = d.DevEUI(context.Background(), "acme", "dev-1")
	require.Equal(t, 1, q.calls)
	_, _ = d.DevEUI(context.Background(), "globex", "dev-1")
	require.Equal(t, 2, q.calls, "another tenant's device with the same token is a different entry")
	now = now.Add(devEUICacheTTL + time.Second)
	_, _ = d.DevEUI(context.Background(), "acme", "dev-1")
	require.Equal(t, 3, q.calls)

	// A missing alias is asked again every time, so adding it takes effect at once.
	q2 := &fakeQuerier{body: `{"devicesByToken":[{"token":"dev-1","aliases":[]}]}`}
	d2 := NewServiceDevices(q2, "u")
	_, _ = d2.DevEUI(context.Background(), "acme", "dev-1")
	_, _ = d2.DevEUI(context.Background(), "acme", "dev-1")
	require.Equal(t, 2, q2.calls)
}
