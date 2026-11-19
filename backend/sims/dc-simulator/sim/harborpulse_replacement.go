// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"context"
	"fmt"
	"time"
)

const (
	HarborpulseReplacementDevice                      = "hp-gauge-north-3"
	HarborpulseReplacementSeparateDevice              = "hp-gauge-north-3-new"
	HarborpulseReplacementExternalID                  = "PAW-GAUGE-NORTH-3"
	HarborpulseReplacementHour                        = 12.5
	HarborpulseDefectReplacementNewDevice             = "replacement-new-device"
	HarborpulseDefectReplacementOldCredentialLeftLive = "replacement-old-credential-left-live"
)

// DeriveDeviceCredential recovers only the original deterministic simulator credential.
// Replacement credentials are random, readable once, and never derived or persisted here.
func DeriveDeviceCredential(seed int64, device string) (string, string) {
	return deriveCredential(seed, device)
}

func HarborpulseReplacementUnit(seed int64) string {
	return fmt.Sprintf("PAW-SN-NORTH-3-%x", uint64(seed))
}

// Caller holds devicesMu, including during Provision.
func (rt *Runtime) applyCredentialOverrides(devices []DeviceInstance) []DeviceInstance {
	for i, d := range devices {
		if replacement, ok := rt.credentialOverrides[d.Token]; ok {
			devices[i] = replacement
		}
	}
	return devices
}

const queryHarborReplacements = `query($c:DeviceReplacementSearchCriteria!){deviceReplacements(criteria:$c){results{occurredTime unitIdentifier retiredCredentialTokens newCredentialToken device{id token externalId}} pagination{totalRecords}}}`
const mutationHarborReplace = `mutation($request:DeviceReplaceRequest!){replaceDevice(request:$request){replacement{occurredTime unitIdentifier retiredCredentialTokens newCredentialToken device{id token externalId}} newCredential{token credentialId}}}`
const mutationHarborCredentialEnabled = `mutation($token:String!,$request:DeviceCredentialUpdateRequest!){updateDeviceCredential(token:$token,request:$request){token}}`

type harborReplacementRecord struct {
	OccurredTime, UnitIdentifier, NewCredentialToken string
	RetiredCredentialTokens                          []string
	Device                                           struct{ ID, Token, ExternalID string }
}

func harborReplacementHistory(ctx context.Context, rt *Runtime) (int, error) {
	var got struct {
		DeviceReplacements *struct {
			Results    []harborReplacementRecord
			Pagination struct{ TotalRecords *int }
		}
	}
	err := rt.Session.Query(ctx, rt.Endpoints.DeviceMgmtGraphQL, queryHarborReplacements,
		map[string]any{"c": map[string]any{"pageNumber": 1, "pageSize": 1, "device": HarborpulseReplacementDevice}}, &got)
	if err != nil {
		return 0, err
	}
	if got.DeviceReplacements == nil || got.DeviceReplacements.Pagination.TotalRecords == nil {
		return 0, fmt.Errorf("replacement history unavailable for %s", HarborpulseReplacementDevice)
	}
	return *got.DeviceReplacements.Pagination.TotalRecords, nil
}

// Run BEFORE Provision: a fresh process must not recreate a missing retired seed credential.
func (s *harborpulse) prepareReplacement(ctx context.Context, rt *Runtime) error {
	rt.devicesMu.Lock()
	defer rt.devicesMu.Unlock()
	n, err := harborReplacementHistory(ctx, rt)
	if err != nil {
		return err
	}
	if n > 0 {
		if d, ok := rt.credentialOverrides[HarborpulseReplacementDevice]; !ok || d.CredentialId == "" {
			if rt.pausedDevices == nil {
				rt.pausedDevices = map[string]bool{}
			}
			rt.pausedDevices[HarborpulseReplacementDevice] = true
			return fmt.Errorf("%s: replacement credential was readable once and is unavailable in this process; refusing retired credential", HarborpulseReplacementDevice)
		}
	}
	return nil
}

func setHarborCredentialEnabled(ctx context.Context, rt *Runtime, token string, enabled bool) error {
	var got any
	return rt.Session.Query(ctx, rt.Endpoints.DeviceMgmtGraphQL, mutationHarborCredentialEnabled,
		map[string]any{"token": token, "request": map[string]any{"enabled": enabled}}, &got)
}

func (s *harborpulse) replacementBeat(ctx context.Context, rt *Runtime, now time.Time) error {
	if rt.Session == nil || rt.Endpoints.DeviceMgmtGraphQL == "" {
		return nil // telemetry-only fixtures have no device-management plane
	}
	started := s.startedAt()
	if started.IsZero() || now.Before(harborWall(started, s.dayLength, 0, HarborpulseReplacementHour)) {
		return nil
	}
	// Exclude outstanding emits as well as readers of the slice. No old request can
	// escape between retiring the credential and installing its replacement.
	rt.devicesMu.Lock()
	defer rt.devicesMu.Unlock()
	s.s5.mu.Lock()
	defer s.s5.mu.Unlock()
	if s.s5.ledger == nil || s.s5.ledger.OTA.Artifact == "" {
		return nil // no replacement population before scenario bootstrap
	}
	if s.s5.replacementDone {
		return nil
	}
	index := -1
	for i, d := range rt.Devices {
		if d.Token == HarborpulseReplacementDevice || d.Token == HarborpulseReplacementSeparateDevice {
			index = i
			break
		}
	}
	if index < 0 {
		return nil
	} // telemetry-only unit fixtures
	if rt.pausedDevices == nil {
		rt.pausedDevices = map[string]bool{}
	}
	if rt.credentialOverrides == nil {
		rt.credentialOverrides = map[string]DeviceInstance{}
	}
	old := rt.Devices[index]
	if s.s5.replacementOld != nil {
		old = *s.s5.replacementOld
	} else {
		saved := old
		s.s5.replacementOld = &saved
	}
	rt.pausedDevices[old.Token] = true
	if s.s5.ledger == nil {
		s.s5.ledger = &ScenarioS5{}
	}
	next, installed := rt.credentialOverrides[HarborpulseReplacementDevice]
	if !installed {
		next = old
		if s.defects.replacementNewDevice {
			next.Token = HarborpulseReplacementSeparateDevice
			next.ExternalId = "PAW-GAUGE-NORTH-3-NEW"
			next.CredentialToken, next.CredentialId = deriveCredential(s.seed, next.Token)
			if err := ensureDevice(ctx, rt, next); err != nil {
				return err
			}
			s.s5.ledger.Replacement = &ScenarioReplacement{Device: next.Token, At: now.UTC().Format(time.RFC3339Nano), UnitIdentifier: HarborpulseReplacementUnit(s.seed), RetiredCredentialTokens: []string{old.CredentialToken}, NewCredentialToken: next.CredentialToken}
		} else {
			n, err := harborReplacementHistory(ctx, rt)
			if err != nil {
				return err
			}
			if n > 0 {
				return fmt.Errorf("%s paused: replacement exists but once-readable credential unavailable", old.Token)
			}
			var got struct {
				ReplaceDevice *struct {
					Replacement   harborReplacementRecord
					NewCredential struct{ Token, CredentialID string }
				}
			}
			err = rt.Session.Query(ctx, rt.Endpoints.DeviceMgmtGraphQL, mutationHarborReplace, map[string]any{"request": map[string]any{
				"deviceToken": old.Token, "credentialType": credentialTypeAccessToken, "credentialToken": old.Token + "-replacement", "reason": "water ingress", "unitIdentifier": HarborpulseReplacementUnit(s.seed),
			}}, &got)
			if err != nil {
				return err
			}
			if got.ReplaceDevice == nil || got.ReplaceDevice.NewCredential.CredentialID == "" {
				return fmt.Errorf("%s paused: replacement returned no credential", old.Token)
			}
			r := got.ReplaceDevice.Replacement
			if r.Device.Token != old.Token || r.Device.ExternalID != old.ExternalId || r.NewCredentialToken != got.ReplaceDevice.NewCredential.Token {
				return fmt.Errorf("replacement returned inconsistent identity")
			}
			next.CredentialToken = r.NewCredentialToken
			next.CredentialId = got.ReplaceDevice.NewCredential.CredentialID
			s.s5.ledger.Replacement = &ScenarioReplacement{Device: old.Token, At: r.OccurredTime, UnitIdentifier: r.UnitIdentifier, RetiredCredentialTokens: r.RetiredCredentialTokens, NewCredentialToken: r.NewCredentialToken}
		}
		// Save the once-returned secret before any fallible reconnect or control write.
		rt.credentialOverrides[HarborpulseReplacementDevice] = next
		rt.Devices[index] = next
	}
	rt.pausedDevices[next.Token] = true
	if s.defects.replacementNewDevice {
		if err := setHarborCredentialEnabled(ctx, rt, old.CredentialToken, false); err != nil {
			return err
		}
	}
	if s.defects.replacementOldCredentialLeftLive {
		for _, token := range s.s5.ledger.Replacement.RetiredCredentialTokens {
			if err := setHarborCredentialEnabled(ctx, rt, token, true); err != nil {
				return err
			}
		}
	}
	// The gauge has a command subscription; the scenario OTA population contains chargers only.
	if rt.reconnectDevice != nil {
		if err := rt.reconnectDevice(ctx, old, next); err != nil {
			return err
		}
	}
	delete(rt.pausedDevices, old.Token)
	delete(rt.pausedDevices, next.Token)
	s.s5.replacementDone = true
	return nil
}
