// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package scenarioverify

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-simulator/sim"
)

const (
	InvChargerMQTTStored    = "charger-mqtt-stored"
	InvChargerX509Admitted  = "charger-x509-admitted"
	InvConnectivityAlarm    = "connectivity-alarm"
	InvConfigApplied        = "config-applied"
	InvPullCommandsComplete = "pull-commands-complete"
)

type S6Presence struct {
	DeviceToken                                string
	Active                                     bool
	PresenceSource, SessionID, LastConnectTime string
}
type S6Credential struct {
	Token, CredentialType, UpdatedAt string
	CredentialID                     string `json:"credentialId"`
	Enabled                          bool
}
type S6Revision struct {
	Revision         int
	Digest, Document string
	CreatedAt        string
}
type S6Report struct {
	Revision       int
	Digest, Status string
	Errors         []string
	ReportedAt     string
}
type S6Configuration struct {
	Desired        *S6Revision
	Reported       *S6Report
	Pending, Stale bool
}

// S6 combines tenant-plane evidence with rejection records from platform callout logs.
// Ledger contains only the expected public identities.
type S6Rejection struct {
	Event, Tenant, Fingerprint, RejectedAt string
}

type S6 struct {
	Tenant         string
	Rejections     []S6Rejection
	Ledger         *sim.ScenarioS6
	States         map[string]S6Presence
	Credentials    map[string]S6Credential
	AnchorEnabled  bool
	Configurations map[string]S6Configuration
	PullChannel    string
	Readings       map[string][]Reading
	Commands       []Command
}

func init() {
	Controls[InvChargerMQTTStored] = sim.HarborpulseDefectMQTTWrongTopic
	Controls[InvChargerX509Admitted] = sim.HarborpulseDefectX509RevocationSkipped
	Controls[InvConnectivityAlarm] = sim.HarborpulseDefectMQTTNeverDisconnect
	Controls[InvConfigApplied] = sim.HarborpulseDefectConfigReportStale
	Controls[InvPullCommandsComplete] = sim.HarborpulseDefectPullPollingStopped
}

func s6Checks() []struct {
	name string
	fn   func(Observations) error
} {
	return []struct {
		name string
		fn   func(Observations) error
	}{
		{InvChargerMQTTStored, chargerMQTTStored}, {InvChargerX509Admitted, chargerX509Admitted},
		{InvConnectivityAlarm, connectivityAlarm}, {InvConfigApplied, configApplied},
		{InvPullCommandsComplete, pullCommandsComplete},
	}
}

func s6Online(s S6, device string) bool {
	p, ok := s.States[device]
	return ok && p.Active && p.PresenceSource == "ASSERTED" && p.SessionID != "" && p.SessionID != "0"
}

func chargerMQTTStored(o Observations) error {
	for _, device := range sim.HarborpulseMQTTChargers() {
		if !s6Online(o.S6, device) {
			return fmt.Errorf("%s has no broker-asserted ONLINE session", device)
		}
		n := 0
		for _, r := range o.S6.Readings[device] {
			if r.Name == sim.HarborpulseChargerPowerKey && r.Value == 7.2 && s6InDay(o, r.At) {
				n++
			}
		}
		if n < 3 {
			return fmt.Errorf("%s has %d stored MQTT power readings, want at least three", device, n)
		}
	}
	return nil
}

func chargerX509Admitted(o Observations) error {
	s := o.S6
	if s.Ledger == nil || s.Ledger.Anchor == "" || !s.AnchorEnabled {
		return fmt.Errorf("no enabled platform trust anchor for certificate chargers")
	}
	revokedAt := o.at(sim.HarborpulseX509RevokeHour)
	for _, device := range sim.HarborpulseX509Chargers() {
		credential, ok := s.Credentials[s.Ledger.Credentials[device]]
		if !ok || credential.CredentialType != "X509_CERTIFICATE" {
			return fmt.Errorf("%s has no pinned certificate credential", device)
		}
		// Provision's ordinary token must be disabled: no certificate charger may silently
		// succeed through the token-authenticated path while its certificate is refused.
		fallback, present := s.Credentials["token/"+device]
		if !present || fallback.Enabled {
			return fmt.Errorf("%s still has an enabled token fallback", device)
		}
		before, after := 0, 0
		for _, row := range s.Readings[device] {
			if row.Name != sim.HarborpulseChargerPowerKey || row.Value != 7.2 || !s6InDay(o, row.At) {
				continue
			}
			if row.At.Before(revokedAt) {
				before++
			} else if row.At.After(revokedAt.Add(15 * time.Second)) {
				after++
			}
		}
		if before < 3 {
			return fmt.Errorf("%s did not store certificate-authenticated telemetry before revocation", device)
		}
		if device == sim.HarborpulseRevokedCharger() {
			state, exists := s.States[device]
			if credential.Enabled || !exists || state.Active || state.PresenceSource != "ASSERTED" || after != 0 {
				return fmt.Errorf("revoked charger: credentialEnabled=%t presenceFound=%t active=%t presenceSource=%q storedAfterRevocation=%d", credential.Enabled, exists, state.Active, state.PresenceSource, after)
			}
			// Bootstrap connects before the first tick starts the scenario clock.
			// The stored pre-revocation readings above prove this session was used
			// during the day; its last admission must precede credential disable.
			disabled, disableErr := time.Parse(time.RFC3339Nano, credential.UpdatedAt)
			connected, connectErr := time.Parse(time.RFC3339Nano, state.LastConnectTime)
			if disableErr != nil || connectErr != nil || disabled.Before(revokedAt) || !s6InDay(o, disabled) || connected.IsZero() || connected.After(disabled) {
				return fmt.Errorf("revoked charger has no platform timestamps proving its last admission preceded credential disable")
			}
			rejected := false
			for _, rejection := range s.Rejections {
				at, err := time.Parse(time.RFC3339Nano, rejection.RejectedAt)
				if rejection.Event == "x509_credential_rejected" && s.Tenant != "" && rejection.Tenant == s.Tenant && credential.CredentialID != "" && rejection.Fingerprint == credential.CredentialID && err == nil && at.After(disabled) && s6InDay(o, at) {
					rejected = true
				}
			}
			if !rejected {
				return fmt.Errorf("revoked certificate has no platform callout rejection after disable on day %d", o.Day)
			}
		} else if !credential.Enabled || !s6Online(s, device) || after < 3 {
			return fmt.Errorf("healthy certificate charger %s did not remain admitted", device)
		}
	}
	return nil
}

func connectivityAlarm(o Observations) error {
	device := sim.HarborpulseMQTTChargers()[0]
	from, to := o.at(sim.HarborpulseMQTTDropFrom), o.at(sim.HarborpulseMQTTDropTo)
	var token string
	for _, a := range samples(o, from, to, func(a AlarmSample) bool {
		return a.Device == device && a.Key == sim.HarborpulseConnectivityAlarm && a.State == sim.AlarmStateActiveWire
	}) {
		token = a.Token
		break
	}
	if token == "" {
		return fmt.Errorf("planted MQTT session drop never raised connectivity alarm")
	}
	for _, a := range samples(o, to, to.Add(30*time.Second), func(a AlarmSample) bool { return a.Token == token && a.State == sim.AlarmStateClearedWire }) {
		if !a.At.Before(to) {
			return nil
		}
	}
	return fmt.Errorf("connectivity alarm did not clear on the session's reconnect")
}

func configApplied(o Observations) error {
	for _, device := range sim.HarborpulseX509Chargers() {
		c, ok := o.S6.Configurations[device]
		if !ok || c.Desired == nil || c.Reported == nil || c.Stale || c.Desired.Revision < 2 {
			return fmt.Errorf("%s has no current pushed configuration revision and report", device)
		}
		d, r := c.Desired, c.Reported
		minted, mintErr := time.Parse(time.RFC3339Nano, d.CreatedAt)
		reported, reportErr := time.Parse(time.RFC3339Nano, r.ReportedAt)
		if mintErr != nil || reportErr != nil || minted.Before(o.at(sim.HarborpulseConfigHour)) || !s6InDay(o, minted) || reported.Before(minted) || !s6InDay(o, reported) {
			return fmt.Errorf("%s has no configuration transition and report on day %d: createdAt=%q reportedAt=%q", device, o.Day, d.CreatedAt, r.ReportedAt)
		}
		var document map[string]float64
		if err := json.Unmarshal([]byte(d.Document), &document); err != nil || len(document) != 1 || document[sim.HarborpulseConfigKey] != 11 {
			return fmt.Errorf("%s desired document does not contain the pushed max_kw=11", device)
		}
		hash := sha256.Sum256([]byte(d.Document))
		if d.Digest != "sha256:"+hex.EncodeToString(hash[:]) || r.Revision != d.Revision || r.Digest != d.Digest {
			return fmt.Errorf("%s report does not match the desired revision and digest", device)
		}
		if device == sim.HarborpulseRejectedConfigCharger() {
			if r.Status != "REJECTED" || len(r.Errors) == 0 || !c.Pending {
				return fmt.Errorf("planted charger did not report REJECTED at the current revision")
			}
		} else if r.Status != "APPLIED" || c.Pending {
			return fmt.Errorf("%s did not apply its current desired revision", device)
		}
	}
	return nil
}

func pullCommandsComplete(o Observations) error {
	if o.S6.PullChannel != "HTTP_PULL" {
		return fmt.Errorf("lamp type command channel is %q", o.S6.PullChannel)
	}
	for _, device := range sim.HarborpulsePullLights() {
		var answered time.Time
		for _, c := range o.S6.Commands {
			if c.Device == device && c.Name == sim.HarborpulseSetDimKey && c.Status == "SUCCESSFUL" && !c.Responded.IsZero() && !c.Queued.Before(o.at(sim.HarborpulsePullHour)) && s6InDay(o, c.Queued) && !c.Responded.Before(c.Queued) && s6InDay(o, c.Responded) {
				answered = c.Responded
				break
			}
		}
		if answered.IsZero() {
			return fmt.Errorf("%s pull command never completed", device)
		}
		acted := false
		for _, r := range o.S6.Readings[device] {
			acted = acted || r.Name == sim.HarborpulseDimKey && r.Value == 35 && !r.At.Before(answered) && s6InDay(o, r.At)
		}
		if !acted {
			return fmt.Errorf("%s did not store the pulled command's applied dim level", device)
		}
	}
	return nil
}

// s6InDay excludes evidence from earlier and later scenario days, including
// delayed reads collected while the next day is already running.
func s6InDay(o Observations, at time.Time) bool {
	return !at.Before(o.Clock.StartedAt) && !at.Before(o.at(0)) && at.Before(o.at(24))
}
