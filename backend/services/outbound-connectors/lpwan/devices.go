// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package lpwan

import (
	"context"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/devicechain-io/dc-microservice/cmddownlink"
	"github.com/devicechain-io/dc-microservice/devicealias"
)

var errRecordNotFound = gorm.ErrRecordNotFound

// DevEUIQuery reads one device's aliases from device-management. It is the whole of what the
// adapter asks that service: the DevEUI is never taken from the command.
const DevEUIQuery = `query($tokens: [String!]!) {
  devicesByToken(tokens: $tokens) { token aliases { scheme value } }
}`

const (
	// devEUICacheTTL bounds how long a device's DevEUI is reused. A DevEUI changes only when
	// someone edits the device's aliases.
	devEUICacheTTL = 60 * time.Second
	devEUICacheMax = 4096
)

type devEUIKey struct{ tenant, device string }

type devEUIEntry struct {
	eui     string
	expires time.Time
}

// ServiceDevices reads DevEUIs over device-management's GraphQL API through the same Querier seam
// the claimer uses.
type ServiceDevices struct {
	client  cmddownlink.Querier
	baseURL string
	now     func() time.Time

	mu    sync.Mutex
	cache map[devEUIKey]devEUIEntry
}

// NewServiceDevices builds the reader over a service-token client and device-management's URL.
func NewServiceDevices(client cmddownlink.Querier, baseURL string) *ServiceDevices {
	return &ServiceDevices{client: client, baseURL: baseURL, now: time.Now, cache: map[devEUIKey]devEUIEntry{}}
}

// DevEUI implements Devices. Only a found DevEUI is cached; "no such alias" is asked again each
// time, so adding the alias takes effect on the next command.
func (d *ServiceDevices) DevEUI(ctx context.Context, tenant, deviceToken string) (string, error) {
	key := devEUIKey{tenant, deviceToken}
	d.mu.Lock()
	if e, ok := d.cache[key]; ok && d.now().Before(e.expires) {
		d.mu.Unlock()
		return e.eui, nil
	}
	d.mu.Unlock()

	var resp struct {
		Devices []struct {
			Token   string `json:"token"`
			Aliases *[]struct {
				Scheme string `json:"scheme"`
				Value  string `json:"value"`
			} `json:"aliases"`
		} `json:"devicesByToken"`
	}
	if err := d.client.Query(ctx, d.baseURL, tenant, DevEUIQuery, map[string]any{"tokens": []string{deviceToken}}, &resp); err != nil {
		return "", err
	}
	for _, dev := range resp.Devices {
		if dev.Token != deviceToken || dev.Aliases == nil {
			continue
		}
		for _, a := range *dev.Aliases {
			if a.Scheme != devicealias.SchemeDevEUI {
				continue
			}
			eui := strings.ToUpper(a.Value)
			if !validDevEUI(eui) {
				return "", ErrNoDevEUI
			}
			d.mu.Lock()
			if len(d.cache) >= devEUICacheMax {
				d.cache = map[devEUIKey]devEUIEntry{}
			}
			d.cache[key] = devEUIEntry{eui: eui, expires: d.now().Add(devEUICacheTTL)}
			d.mu.Unlock()
			return eui, nil
		}
	}
	return "", ErrNoDevEUI
}
