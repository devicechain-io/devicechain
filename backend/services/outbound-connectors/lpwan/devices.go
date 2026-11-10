// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package lpwan

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/devicechain-io/dc-microservice/cmddownlink"
	"github.com/devicechain-io/dc-microservice/devicealias"
	"github.com/devicechain-io/dc-microservice/payloadencode"
)

var errRecordNotFound = gorm.ErrRecordNotFound

// DevEUIQuery reads one device's aliases from device-management.
// The DevEUI is never taken from the command.
const DevEUIQuery = `query($tokens: [String!]!) {
  devicesByToken(tokens: $tokens) { token aliases { scheme value } }
}`

const (
	// devEUICacheTTL bounds how long a device's DevEUI is reused. A DevEUI changes only when
	// someone edits the device's aliases.
	devEUICacheTTL = 60 * time.Second
	devEUICacheMax = 4096
)

type devEUIKey struct{ tenant, device, scheme string }

type devEUIEntry struct {
	eui     string
	expires time.Time
}

// ServiceDevices reads aliases and published encoders over device-management's GraphQL API
// through the same Querier seam the claimer uses.
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
	return d.alias(ctx, tenant, deviceToken, devicealias.SchemeDevEUI, ErrNoDevEUI)
}

// TTSDeviceID implements Devices: the device's tts.device-id alias, the name The Things Stack's
// downlink API addresses it by. It is read from the device, never from the command.
func (d *ServiceDevices) TTSDeviceID(ctx context.Context, tenant, deviceToken string) (string, error) {
	return d.alias(ctx, tenant, deviceToken, devicealias.SchemeTTSDeviceID, ErrNoDeviceID)
}

func (d *ServiceDevices) alias(ctx context.Context, tenant, deviceToken, scheme string, absent error) (string, error) {
	key := devEUIKey{tenant, deviceToken, scheme}
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
			if a.Scheme != scheme {
				continue
			}
			eui, nerr := devicealias.Normalize(scheme, a.Value)
			if nerr != nil || (scheme == devicealias.SchemeDevEUI && !validDevEUI(eui)) {
				return "", absent
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
	return "", absent
}

const EncoderQuery = `query($deviceToken: String!) {
 deviceCommandVocabulary(deviceToken: $deviceToken) { constrained commands { commandKey encoder } }
}`

// ErrDefinitiveDefinition marks a published definition that cannot be dispatched.
// Read/query failures do not wrap it and must be retried.
var ErrDefinitiveDefinition = errors.New("invalid published command definition")

// Encoder reads the published vocabulary anew for every dispatch, including raw commands.
func (d *ServiceDevices) Encoder(ctx context.Context, tenant, deviceToken, commandKey string) (*payloadencode.Encoder, error) {
	var resp struct {
		Vocabulary *struct {
			Constrained *bool `json:"constrained"`
			Commands    *[]struct {
				Key     string  `json:"commandKey"`
				Encoder *string `json:"encoder"`
			} `json:"commands"`
		} `json:"deviceCommandVocabulary"`
	}
	if err := d.client.Query(ctx, d.baseURL, tenant, EncoderQuery, map[string]any{"deviceToken": deviceToken}, &resp); err != nil {
		return nil, err
	}
	if resp.Vocabulary == nil || resp.Vocabulary.Constrained == nil || resp.Vocabulary.Commands == nil {
		return nil, fmt.Errorf("published vocabulary is unavailable")
	}
	var encoder *payloadencode.Encoder
	found := false
	for _, c := range *resp.Vocabulary.Commands {
		if c.Key != commandKey {
			continue
		}
		if found {
			return nil, fmt.Errorf("%w: ambiguous command definition", ErrDefinitiveDefinition)
		}
		found = true
		if c.Encoder != nil {
			var err error
			encoder, err = payloadencode.Parse([]byte(*c.Encoder))
			if err != nil {
				return nil, fmt.Errorf("%w: %v", ErrDefinitiveDefinition, err)
			}
		}
	}
	if *resp.Vocabulary.Constrained && !found {
		return nil, fmt.Errorf("%w: published command is unavailable", ErrDefinitiveDefinition)
	}
	return encoder, nil
}
