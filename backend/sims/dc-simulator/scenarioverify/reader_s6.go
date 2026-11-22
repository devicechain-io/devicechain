// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package scenarioverify

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/devicechain-io/dc-simulator/sim"
)

const queryS6States = `query($tokens:[String!]!){deviceStatesByDeviceToken(deviceTokens:$tokens){deviceToken active presenceSource sessionId lastConnectTime}}`
const queryS6Credentials = `query($tokens:[String!]!){deviceCredentialsByToken(tokens:$tokens){token credentialType credentialId enabled updatedAt}}`
const queryS6Anchor = `query($tokens:[String!]!){deviceTrustAnchorsByToken(tokens:$tokens){token enabled}}`
const queryS6Configuration = `query($device:String!){deviceConfiguration(deviceToken:$device){desired{revision digest document createdAt} reported{revision digest status errors reportedAt} pending stale}}`
const queryS6PullType = `query($tokens:[String!]!){deviceTypesByToken(tokens:$tokens){token commandChannel}}`

func readS6(ctx context.Context, r *Reader, o *Observations, ledger *sim.ScenarioS6) error {
	if ledger == nil {
		return fmt.Errorf("harborpulse has no S6 declaration")
	}
	if r.endpoints.DeviceStateGraphQL == "" {
		return fmt.Errorf("handshake has no deviceStateGraphQL for MQTT presence")
	}
	s := S6{Tenant: r.tenant, Ledger: ledger, States: map[string]S6Presence{}, Credentials: map[string]S6Credential{}, Configurations: map[string]S6Configuration{}, Readings: map[string][]Reading{}}
	chargers := append(sim.HarborpulseMQTTChargers(), sim.HarborpulseX509Chargers()...)
	var states struct {
		States []S6Presence `json:"deviceStatesByDeviceToken"`
	}
	if err := r.session.Query(ctx, r.endpoints.DeviceStateGraphQL, queryS6States, map[string]any{"tokens": chargers}, &states); err != nil {
		return err
	}
	for _, state := range states.States {
		if _, duplicate := s.States[state.DeviceToken]; duplicate {
			return fmt.Errorf("duplicate MQTT presence for %s", state.DeviceToken)
		}
		s.States[state.DeviceToken] = state
	}
	var tokens []string
	fallbacks := map[string]string{}
	for _, device := range sim.HarborpulseX509Chargers() {
		fallback, _ := sim.DeriveDeviceCredential(r.seed, device)
		tokens = append(tokens, ledger.Credentials[device], fallback)
		fallbacks[fallback] = device
	}
	var credentials struct {
		Items []S6Credential `json:"deviceCredentialsByToken"`
	}
	if err := r.session.Query(ctx, r.endpoints.DeviceMgmtGraphQL, queryS6Credentials, map[string]any{"tokens": tokens}, &credentials); err != nil {
		return err
	}
	for _, c := range credentials.Items {
		s.Credentials[c.Token] = c
		if d := fallbacks[c.Token]; d != "" {
			s.Credentials["token/"+d] = c
		}
	}
	var anchors struct {
		Items []struct {
			Token   string
			Enabled bool
		} `json:"deviceTrustAnchorsByToken"`
	}
	if err := r.session.Query(ctx, r.endpoints.DeviceMgmtGraphQL, queryS6Anchor, map[string]any{"tokens": []string{ledger.Anchor}}, &anchors); err != nil {
		return err
	}
	s.AnchorEnabled = len(anchors.Items) == 1 && anchors.Items[0].Token == ledger.Anchor && anchors.Items[0].Enabled
	for _, device := range sim.HarborpulseX509Chargers() {
		var out struct {
			Configuration *S6Configuration `json:"deviceConfiguration"`
		}
		if err := r.session.Query(ctx, r.endpoints.DeviceMgmtGraphQL, queryS6Configuration, map[string]any{"device": device}, &out); err != nil {
			return err
		}
		if out.Configuration != nil {
			s.Configurations[device] = *out.Configuration
		}
	}
	var typ struct {
		Items []struct{ Token, CommandChannel string } `json:"deviceTypesByToken"`
	}
	if err := r.session.Query(ctx, r.endpoints.DeviceMgmtGraphQL, queryS6PullType, map[string]any{"tokens": []string{sim.HarborpulsePullLightType}}, &typ); err != nil {
		return err
	}
	if len(typ.Items) == 1 && typ.Items[0].Token == sim.HarborpulsePullLightType {
		s.PullChannel = typ.Items[0].CommandChannel
	}
	for _, device := range append(chargers, sim.HarborpulsePullLights()...) {
		rows, err := r.Readings(ctx, device, s6DayStart(*o), o.at(WatchTo))
		if err != nil {
			return err
		}
		s.Readings[device] = rows
	}
	for _, device := range sim.HarborpulsePullLights() {
		cmds, err := r.Commands(ctx, device)
		if err != nil {
			return err
		}
		s.Commands = append(s.Commands, cmds...)
	}
	o.S6 = s
	return nil
}

func s6DayStart(o Observations) time.Time {
	start := o.at(0)
	if start.Before(o.Clock.StartedAt) {
		return o.Clock.StartedAt
	}
	return start
}

// Only the platform's structured disabled-certificate records are evidence. Kubectl
// diagnostics and other tenants' log lines cannot satisfy this tenant's invariant.
func readS6Rejections(path, tenant string) ([]S6Rejection, error) {
	if path == "" {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	var records []S6Rejection
	for scanner.Scan() {
		var record S6Rejection
		if json.Unmarshal(scanner.Bytes(), &record) == nil && record.Event == "x509_credential_rejected" && record.Tenant == tenant {
			records = append(records, record)
		}
	}
	return records, scanner.Err()
}
