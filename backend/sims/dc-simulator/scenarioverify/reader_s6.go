// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package scenarioverify

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-simulator/sim"
)

const queryS6States = `query($tokens:[String!]!){deviceStatesByDeviceToken(deviceTokens:$tokens){deviceToken active presenceSource sessionId lastConnectTime}}`
const queryS6Credentials = `query($tokens:[String!]!){deviceCredentialsByToken(tokens:$tokens){token credentialType enabled updatedAt}}`
const queryS6Anchor = `query($tokens:[String!]!){deviceTrustAnchorsByToken(tokens:$tokens){token enabled}}`
const queryS6Configuration = `query($device:String!){deviceConfiguration(deviceToken:$device){desired{revision digest document} reported{revision digest status errors} pending stale}}`
const queryS6PullType = `query($tokens:[String!]!){deviceTypesByToken(tokens:$tokens){token commandChannel}}`

func readS6(ctx context.Context, r *Reader, o *Observations, ledger *sim.ScenarioS6) error {
	if ledger == nil {
		return fmt.Errorf("harborpulse has no S6 declaration")
	}
	if r.endpoints.DeviceStateGraphQL == "" {
		return fmt.Errorf("handshake has no deviceStateGraphQL for MQTT presence")
	}
	s := S6{Ledger: ledger, States: map[string]S6Presence{}, Credentials: map[string]S6Credential{}, Configurations: map[string]S6Configuration{}, Readings: map[string][]Reading{}}
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
		rows, err := r.Readings(ctx, device, o.Clock.StartedAt, o.at(WatchTo))
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
