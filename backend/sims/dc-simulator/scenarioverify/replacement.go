// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package scenarioverify

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/devicechain-io/dc-simulator/sim"
)

const (
	InvReplacementIdentity      = "replacement-identity"
	InvReplacementCredential    = "replacement-credential"
	queryReplacementDevice      = `query($tokens:[String!]!){devicesByToken(tokens:$tokens){id token externalId}}`
	queryReplacements           = `query($c:DeviceReplacementSearchCriteria!){deviceReplacements(criteria:$c){results{occurredTime unitIdentifier retiredCredentialTokens newCredentialToken device{id token externalId}} pagination{totalRecords}}}`
	queryReplacementCredentials = `query($tokens:[String!]!){deviceCredentialsByToken(tokens:$tokens){token enabled device{token}}}`
)

type ReplacementDevice struct{ ID, Token, ExternalID string }
type ReplacementRecord struct {
	OccurredTime, UnitIdentifier, NewCredentialToken string
	RetiredCredentialTokens                          []string
	Device                                           ReplacementDevice
}
type ReplacementCredential struct {
	Token   string
	Enabled bool
	Device  struct{ Token string }
}
type ReplacementEvidence struct {
	Device      *ReplacementDevice
	Records     []ReplacementRecord
	Credentials map[string]ReplacementCredential
	Readings    []Reading
	ProbeStatus int
	ProbeAt     time.Time
}

func init() {
	Controls[InvReplacementIdentity] = sim.HarborpulseDefectReplacementNewDevice
	Controls[InvReplacementCredential] = sim.HarborpulseDefectReplacementOldCredentialLeftLive
}

func replacementIdentity(o Observations) error {
	s := o.S5.Replacement
	if o.S5.Ledger == nil || o.S5.Ledger.Replacement == nil {
		return fmt.Errorf("no replacement declaration")
	}
	l := o.S5.Ledger.Replacement
	if s.Device == nil || s.Device.Token != sim.HarborpulseReplacementDevice || s.Device.ID == "" || s.Device.ExternalID != sim.HarborpulseReplacementExternalID {
		return fmt.Errorf("logical gauge identity differs")
	}
	if len(s.Records) != 1 {
		return fmt.Errorf("replacement records for this run=%d, want exactly one", len(s.Records))
	}
	r := s.Records[0]
	switch {
	case r.Device != *s.Device:
		return fmt.Errorf("the replacement record names device %+v, the logical gauge is %+v", r.Device, *s.Device)
	case r.UnitIdentifier != sim.HarborpulseReplacementUnit(o.S3.Seed) || l.UnitIdentifier != r.UnitIdentifier:
		return fmt.Errorf("replacement unitIdentifier %q (declared %q)", r.UnitIdentifier, l.UnitIdentifier)
	case l.Device != s.Device.Token:
		return fmt.Errorf("the run declared replacing %s, not %s", l.Device, s.Device.Token)
	}
	// The instant is compared as an instant: the mutation's answer carries the server clock's
	// precision and the stored record the store's (microseconds, rounded), so the two strings
	// of one replacement can differ in their last digits — a live run showed it.
	at, err := time.Parse(time.RFC3339Nano, r.OccurredTime)
	declared, derr := time.Parse(time.RFC3339Nano, l.At)
	if err != nil || derr != nil || at.Sub(declared).Abs() > time.Millisecond {
		return fmt.Errorf("replacement occurredTime %q is not the declared %q", r.OccurredTime, l.At)
	}
	beat := o.Clock.At(0, sim.HarborpulseReplacementHour)
	if at.Before(beat) || at.After(beat.Add(30*time.Second)) {
		return fmt.Errorf("replacement outside its scheduled beat")
	}
	// Every half simulated hour through the judged watch must retain readings under
	// the same logical token, including the half hour immediately before and after.
	for h := sim.HarborpulseReplacementHour - 0.5; h < 24*float64(o.Day)+WatchTo; h += 0.5 {
		from, to := o.Clock.At(0, h), o.Clock.At(0, h+0.5)
		found := false
		for _, reading := range s.Readings {
			if reading.Name == sim.HarborpulseWaterLevelKey && !reading.At.Before(from) && reading.At.Before(to) {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("logical gauge has no readings in sim half hour %.1f", h)
		}
	}
	return nil
}

func replacementCredential(o Observations) error {
	if o.S5.Ledger == nil || o.S5.Ledger.Replacement == nil {
		return fmt.Errorf("no replacement declaration")
	}
	l, s := o.S5.Ledger.Replacement, o.S5.Replacement
	old, _ := sim.DeriveDeviceCredential(o.S3.Seed, sim.HarborpulseReplacementDevice)
	retired := l.RetiredCredentialTokens
	if len(retired) == 0 || !sliceHas(retired, old) {
		return fmt.Errorf("original credential absent from retirement ledger")
	}
	// Membership/absence of the operation belongs to replacement-identity. When
	// present, its credential tokens must match the declaration exactly.
	if len(s.Records) > 0 {
		r := s.Records[0]
		if r.NewCredentialToken != l.NewCredentialToken || len(r.RetiredCredentialTokens) != len(retired) {
			return fmt.Errorf("replacement credential tokens differ from ledger")
		}
		for _, token := range r.RetiredCredentialTokens {
			if !sliceHas(retired, token) {
				return fmt.Errorf("retired token differs from ledger")
			}
		}
	}
	for _, token := range retired {
		c, ok := s.Credentials[token]
		if !ok || c.Enabled || c.Device.Token != sim.HarborpulseReplacementDevice {
			return fmt.Errorf("retired credential %s is missing, enabled or belongs to another device", token)
		}
	}
	if s.ProbeStatus != http.StatusUnauthorized {
		return fmt.Errorf("old credential ingress probe=%d, want 401", s.ProbeStatus)
	}
	c, ok := s.Credentials[l.NewCredentialToken]
	if !ok || !c.Enabled || c.Device.Token != l.Device || sliceHas(retired, c.Token) {
		return fmt.Errorf("new credential is absent, disabled or on another device")
	}
	when, err := time.Parse(time.RFC3339Nano, l.At)
	if err != nil {
		return err
	}
	for _, r := range s.Readings {
		if r.Name == sim.HarborpulseWaterLevelKey && r.At.After(when) {
			return nil
		}
	}
	return fmt.Errorf("no stored readings after replacement")
}

func classifyReplacementProbe(status int) error {
	if status == http.StatusUnauthorized || status == http.StatusAccepted {
		return nil
	}
	return fmt.Errorf("old credential probe HTTP %d: no verdict", status)
}

// A rate refusal precedes authentication at the real ingress. Retry-After must
// elapse before another probe can distinguish a disabled credential from a live one.
func (r *Reader) probeOldCredential(ctx context.Context) (int, time.Time, error) {
	deadline := time.Now().Add(30 * time.Second)
	for {
		now := time.Now().UTC().Truncate(time.Microsecond)
		_, secret := sim.DeriveDeviceCredential(r.seed, sim.HarborpulseReplacementDevice)
		body, err := sim.HTTPMeasurementBody(sim.DeviceInstance{Token: sim.HarborpulseReplacementDevice, CredentialId: secret}, now, map[string]string{sim.HarborpulseBatteryKey: "3.14159"})
		if err != nil {
			return 0, now, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.ingress, bytes.NewReader(body))
		if err != nil {
			return 0, now, err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := r.http.Do(req)
		if err != nil {
			return 0, now, err
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		if resp.StatusCode != http.StatusTooManyRequests {
			return resp.StatusCode, now, classifyReplacementProbe(resp.StatusCode)
		}
		delay := time.Second
		if n, e := strconv.Atoi(resp.Header.Get("Retry-After")); e == nil && n >= 0 {
			delay = time.Duration(n) * time.Second
		} else if at, e := http.ParseTime(resp.Header.Get("Retry-After")); e == nil {
			delay = time.Until(at)
		}
		if delay < time.Millisecond {
			delay = time.Millisecond
		}
		if time.Now().Add(delay).After(deadline) {
			return 0, now, fmt.Errorf("old credential probe remained rate limited: no verdict")
		}
		if err := sleepUntil(ctx, time.Now().Add(delay)); err != nil {
			return 0, now, err
		}
	}
}

func readReplacement(ctx context.Context, r *Reader, o *Observations) error {
	s := &o.S5.Replacement
	var devices struct{ DevicesByToken []ReplacementDevice }
	if err := r.session.Query(ctx, r.endpoints.DeviceMgmtGraphQL, queryReplacementDevice, map[string]any{"tokens": []string{sim.HarborpulseReplacementDevice}}, &devices); err != nil {
		return err
	}
	if len(devices.DevicesByToken) == 1 {
		s.Device = &devices.DevicesByToken[0]
	}
	for page := 1; ; page++ {
		if page > maxPages {
			return fmt.Errorf("replacement history exceeds page bound")
		}
		var got struct {
			DeviceReplacements *struct {
				Results    []ReplacementRecord
				Pagination struct{ TotalRecords *int }
			}
		}
		if err := r.session.Query(ctx, r.endpoints.DeviceMgmtGraphQL, queryReplacements, map[string]any{"c": map[string]any{"pageNumber": page, "pageSize": 100, "device": sim.HarborpulseReplacementDevice}}, &got); err != nil {
			return err
		}
		p := got.DeviceReplacements
		if p == nil || p.Pagination.TotalRecords == nil {
			return fmt.Errorf("null replacement history")
		}
		for _, record := range p.Results {
			at, err := time.Parse(time.RFC3339Nano, record.OccurredTime)
			if err != nil {
				return err
			}
			if !at.Before(o.Clock.StartedAt) {
				s.Records = append(s.Records, record)
			}
		}
		if page*100 >= *p.Pagination.TotalRecords {
			break
		}
		if len(p.Results) == 0 {
			return fmt.Errorf("inconsistent replacement pagination")
		}
	}
	if o.S5.Ledger.Replacement == nil {
		return nil
	}
	l := o.S5.Ledger.Replacement
	if _, err := time.Parse(time.RFC3339Nano, l.At); err != nil {
		return err
	}
	tokens := append([]string(nil), l.RetiredCredentialTokens...)
	tokens = append(tokens, l.NewCredentialToken)
	for _, record := range s.Records {
		tokens = append(tokens, record.RetiredCredentialTokens...)
		tokens = append(tokens, record.NewCredentialToken)
	}
	var creds struct{ DeviceCredentialsByToken []ReplacementCredential }
	if err := r.session.Query(ctx, r.endpoints.DeviceMgmtGraphQL, queryReplacementCredentials, map[string]any{"tokens": tokens}, &creds); err != nil {
		return err
	}
	s.Credentials = map[string]ReplacementCredential{}
	for _, c := range creds.DeviceCredentialsByToken {
		s.Credentials[c.Token] = c
	}
	var err error
	s.ProbeStatus, s.ProbeAt, err = r.probeOldCredential(ctx)
	if err != nil {
		return err
	}
	// A 202 control is a real accepted battery-only event. It cannot change flood
	// levels; read it back too, rather than pretending acceptance is persistence.
	if s.ProbeStatus == http.StatusAccepted {
		stored := false
		for attempt := 0; attempt < 20; attempt++ {
			rows, e := r.Readings(ctx, sim.HarborpulseReplacementDevice, s.ProbeAt, s.ProbeAt.Add(time.Microsecond))
			if e != nil {
				return e
			}
			for _, row := range rows {
				if row.Name == sim.HarborpulseBatteryKey && row.At.Equal(s.ProbeAt) && row.Value == 3.14159 {
					stored = true
				}
			}
			if stored {
				break
			}
			if e := sleepUntil(ctx, time.Now().Add(time.Second)); e != nil {
				return e
			}
		}
		if !stored {
			return fmt.Errorf("accepted old credential probe not yet stored: no verdict")
		}
	}
	s.Readings, err = r.Readings(ctx, l.Device, o.Clock.At(0, 12), o.Clock.At(o.Day, WatchTo))
	return err
}
