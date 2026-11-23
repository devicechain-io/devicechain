// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package scenarioverify

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/httptransport"
	"github.com/devicechain-io/dc-microservice/userclient"
	"github.com/devicechain-io/dc-simulator/sim"
)

// The documents the verifier sends, each validated against the served schema by
// documents_test.go. Every one is a tenant-plane read the sim's own identity may make.
const (
	queryAlarms = `query ScenarioAlarms($c: AlarmSearchCriteria!) {
  alarms(criteria: $c) {
    results { token originatorType originatorId originatorToken alarmKey severity state }
    pagination { totalRecords }
  }
}`

	queryCommands = `query ScenarioCommands($c: CommandSearchCriteria!) {
  commands(criteria: $c) {
    results { deviceToken name status queuedTime respondedTime createdAt updatedAt }
    pagination { totalRecords }
  }
}`

	queryMeasurements = `query ScenarioMeasurements($c: EventSearchCriteria!) {
  measurementEvents(criteria: $c) {
    results { deviceToken occurredTime name value }
    pagination { totalRecords }
  }
}`

	queryPreview = `query ScenarioPreview($input: PreviewRuleInput!) {
  previewRule(input: $input) {
    ok
    degraded
    firings { occurredAt series signal }
    diagnostics { message }
  }
}`
)

const (
	alarmPageSize   = 500
	commandPageSize = 500
	eventPageSize   = 1000
	maxPages        = 50
)

// Reader reads platform truth as the sim's own tenant identity.
type Reader struct {
	session *userclient.TenantSession
	// previewSession is a second sign-in over a transport that outwaits previewRule's server budget.
	previewSession *userclient.TenantSession
	previewClient  *http.Client
	endpoints      sim.Endpoints
	events         string
	ingress        string
	http           *http.Client
	seed           int64
	tenant         string
}

// NewReader signs in as the handshake's sim identity.
func NewReader(hs *sim.Handshake, httpc *http.Client) (*Reader, error) {
	if err := hs.Endpoints.ResolveUpdateManagement(); err != nil {
		return nil, err
	}
	if err := hs.Endpoints.ResolveDeviceState(); err != nil {
		return nil, err
	}
	for name, v := range map[string]string{
		"updateMgmtGraphQL": hs.Endpoints.UpdateMgmtGraphQL,
		"deviceMgmtGraphQL": hs.Endpoints.DeviceMgmtGraphQL, "commandMgmtGraphQL": hs.Endpoints.CommandMgmtGraphQL,
		"eventProcessingGraphQL": hs.Endpoints.EventProcessingGraphQL, "eventMgmtWS": hs.Endpoints.EventMgmtWS,
	} {
		if v == "" {
			return nil, fmt.Errorf("the handshake carries no %s endpoint, which the verifier reads", name)
		}
	}
	pc := previewHTTP(hs.HostHeader)
	return &Reader{
		previewClient: pc,
		http:          httpc, seed: hs.Seed, tenant: hs.Tenant,
		session:        userclient.NewTenantSession(httpc, hs.Endpoints.UserGraphQL, hs.SimEmail, hs.SimPassword, hs.Tenant),
		previewSession: userclient.NewTenantSession(pc, hs.Endpoints.UserGraphQL, hs.SimEmail, hs.SimPassword, hs.Tenant),
		endpoints:      hs.Endpoints,
		events:         httpFromWS(hs.Endpoints.EventMgmtWS),
		ingress:        fmt.Sprintf("%s/%s/%s/events", strings.TrimRight(hs.Endpoints.Ingress, "/"), hs.InstanceId, hs.Tenant),
	}, nil
}

// previewHTTP is the client for previewRule alone. The platform transport waits 30 s for
// response headers, but the server may spend config.PreviewBudget (45 s) on one replay and then
// answers with a typed truncated result; a client that gives up first turns a judgeable day into
// NO VERDICT. Only this call gets the longer wait, derived from the server's own budget.
func previewHTTP(hostHeader string) *http.Client {
	tr := httptransport.New()
	tr.ResponseHeaderTimeout = config.PreviewClientHeaderTimeout
	c := &http.Client{Timeout: config.PreviewClientTimeout, Transport: tr}
	if hostHeader != "" {
		c.Transport = httptransport.PresentHost(tr, hostHeader)
	}
	return c
}

// httpFromWS is event-management's HTTP GraphQL endpoint: the same path as the graphql-ws one.
func httpFromWS(u string) string {
	switch {
	case strings.HasPrefix(u, "wss://"):
		return "https://" + strings.TrimPrefix(u, "wss://")
	case strings.HasPrefix(u, "ws://"):
		return "http://" + strings.TrimPrefix(u, "ws://")
	}
	return u
}

func parseTime(s *string) (time.Time, error) {
	if s == nil || *s == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339Nano, *s)
}

// Alarms reads every alarm of the tenant, stamped with the poll time at.
func (r *Reader) Alarms(ctx context.Context, at time.Time) ([]AlarmSample, error) {
	var out []AlarmSample
	for page := 1; page <= maxPages; page++ {
		var resp struct {
			Alarms struct {
				Results []struct {
					Token           string  `json:"token"`
					OriginatorType  string  `json:"originatorType"`
					OriginatorId    string  `json:"originatorId"`
					OriginatorToken *string `json:"originatorToken"`
					AlarmKey        string  `json:"alarmKey"`
					Severity        string  `json:"severity"`
					State           string  `json:"state"`
				} `json:"results"`
				Pagination struct {
					TotalRecords *int `json:"totalRecords"`
				} `json:"pagination"`
			} `json:"alarms"`
		}
		vars := map[string]any{"c": map[string]any{"pageNumber": page, "pageSize": alarmPageSize}}
		if err := r.session.Query(ctx, r.endpoints.DeviceMgmtGraphQL, queryAlarms, vars, &resp); err != nil {
			return nil, fmt.Errorf("read alarms: %w", err)
		}
		if resp.Alarms.Pagination.TotalRecords == nil {
			return nil, fmt.Errorf("read alarms: the server returned a null totalRecords")
		}
		for _, a := range resp.Alarms.Results {
			device := ""
			if a.OriginatorToken != nil {
				device = *a.OriginatorToken
			}
			out = append(out, AlarmSample{At: at, Token: a.Token, Device: device, Originator: a.OriginatorType + " " + a.OriginatorId,
				Key: a.AlarmKey, Severity: a.Severity, State: a.State})
		}
		if len(out) >= *resp.Alarms.Pagination.TotalRecords || len(resp.Alarms.Results) < alarmPageSize {
			return out, nil
		}
	}
	return nil, fmt.Errorf("read alarms: more than %d pages", maxPages)
}

// Commands reads every command addressed to device.
func (r *Reader) Commands(ctx context.Context, device string) ([]Command, error) {
	var out []Command
	for page := 1; page <= maxPages; page++ {
		var resp struct {
			Commands struct {
				Results []struct {
					DeviceToken   string  `json:"deviceToken"`
					Name          string  `json:"name"`
					Status        string  `json:"status"`
					QueuedTime    *string `json:"queuedTime"`
					RespondedTime *string `json:"respondedTime"`
					CreatedAt     *string `json:"createdAt"`
					UpdatedAt     *string `json:"updatedAt"`
				} `json:"results"`
				Pagination struct {
					TotalRecords *int `json:"totalRecords"`
				} `json:"pagination"`
			} `json:"commands"`
		}
		vars := map[string]any{"c": map[string]any{"pageNumber": page, "pageSize": commandPageSize, "deviceToken": device}}
		if err := r.session.Query(ctx, r.endpoints.CommandMgmtGraphQL, queryCommands, vars, &resp); err != nil {
			return nil, fmt.Errorf("read commands of %s: %w", device, err)
		}
		if resp.Commands.Pagination.TotalRecords == nil {
			return nil, fmt.Errorf("read commands of %s: the server returned a null totalRecords", device)
		}
		for _, c := range resp.Commands.Results {
			queued, err := parseTime(c.QueuedTime)
			if err != nil {
				return nil, fmt.Errorf("command of %s: queuedTime: %w", device, err)
			}
			responded, err := parseTime(c.RespondedTime)
			if err != nil {
				return nil, fmt.Errorf("command of %s: respondedTime: %w", device, err)
			}
			out = append(out, Command{Device: c.DeviceToken, Name: c.Name, Status: c.Status, Queued: queued, Responded: responded, CreatedAt: str(c.CreatedAt), UpdatedAt: str(c.UpdatedAt)})
		}
		if len(out) >= *resp.Commands.Pagination.TotalRecords || len(resp.Commands.Results) < commandPageSize {
			return out, nil
		}
	}
	return nil, fmt.Errorf("read commands of %s: more than %d pages", device, maxPages)
}

// Readings reads every stored measurement of device with an occurred time in [from, to].
func (r *Reader) Readings(ctx context.Context, device string, from, to time.Time) ([]Reading, error) {
	var out []Reading
	// The bounds go out at second precision, so they are widened to whole seconds and the rows
	// are cut to [from, to] here.
	start, end := from.Truncate(time.Second), to.Truncate(time.Second).Add(time.Second)
	for page := 1; page <= maxPages; page++ {
		var resp struct {
			MeasurementEvents struct {
				Results []struct {
					DeviceToken  string   `json:"deviceToken"`
					OccurredTime *string  `json:"occurredTime"`
					Name         string   `json:"name"`
					Value        *float64 `json:"value"`
				} `json:"results"`
				Pagination struct {
					TotalRecords *int `json:"totalRecords"`
				} `json:"pagination"`
			} `json:"measurementEvents"`
		}
		vars := map[string]any{"c": map[string]any{
			"pageNumber": page, "pageSize": eventPageSize, "deviceToken": device,
			"startTime": start.UTC().Format(time.RFC3339), "endTime": end.UTC().Format(time.RFC3339),
		}}
		if err := r.session.Query(ctx, r.events, queryMeasurements, vars, &resp); err != nil {
			return nil, fmt.Errorf("read readings of %s: %w", device, err)
		}
		if resp.MeasurementEvents.Pagination.TotalRecords == nil {
			return nil, fmt.Errorf("read readings of %s: the server returned a null totalRecords", device)
		}
		read := 0
		for _, m := range resp.MeasurementEvents.Results {
			read++
			if m.DeviceToken != device {
				return nil, fmt.Errorf("read readings of %s: the server returned a row of %s", device, m.DeviceToken)
			}
			at, err := parseTime(m.OccurredTime)
			if err != nil || at.IsZero() {
				return nil, fmt.Errorf("read readings of %s: occurredTime %v: %v", device, m.OccurredTime, err)
			}
			if m.Value == nil || at.Before(from) || at.After(to) {
				continue
			}
			out = append(out, Reading{Name: m.Name, At: at, Value: *m.Value})
		}
		if page*eventPageSize >= *resp.MeasurementEvents.Pagination.TotalRecords || read < eventPageSize {
			return out, nil
		}
	}
	return nil, fmt.Errorf("read readings of %s: more than %d pages", device, maxPages)
}

// Preview runs previewRule for a rule definition against profile's history in [from, to].
func (r *Reader) Preview(ctx context.Context, definition, profile string, from, to time.Time) (Preview, error) {
	var resp struct {
		PreviewRule struct {
			OK       bool    `json:"ok"`
			Degraded *string `json:"degraded"`
			Firings  []struct {
				OccurredAt string `json:"occurredAt"`
				Series     string `json:"series"`
				Signal     string `json:"signal"`
			} `json:"firings"`
			Diagnostics []struct {
				Message string `json:"message"`
			} `json:"diagnostics"`
		} `json:"previewRule"`
	}
	vars := map[string]any{"input": map[string]any{
		"ruleDefinition": definition, "profileToken": profile,
		"start": from.UTC().Format(time.RFC3339), "end": to.UTC().Format(time.RFC3339),
	}}
	if err := r.previewSession.Query(ctx, r.endpoints.EventProcessingGraphQL, queryPreview, vars, &resp); err != nil {
		return Preview{}, fmt.Errorf("previewRule: %w", err)
	}
	p := Preview{OK: resp.PreviewRule.OK}
	if resp.PreviewRule.Degraded != nil {
		p.Degraded = *resp.PreviewRule.Degraded
	}
	if !p.OK {
		raw, _ := json.Marshal(resp.PreviewRule.Diagnostics)
		p.Degraded = "refused: " + string(raw)
	}
	for _, f := range resp.PreviewRule.Firings {
		at, err := time.Parse(time.RFC3339Nano, f.OccurredAt)
		if err != nil {
			return Preview{}, fmt.Errorf("previewRule: firing occurredAt %q: %w", f.OccurredAt, err)
		}
		p.Firings = append(p.Firings, Firing{At: at, Series: f.Series, Signal: f.Signal})
	}
	return p, nil
}

// Status is the part of the sim's GET /status the verifier reads.
type Status struct {
	State    string             `json:"state"`
	Tenant   string             `json:"tenant"`
	Scenario *sim.ScenarioClock `json:"scenario"`
}

// ReadStatus reads the sim's control API.
func ReadStatus(ctx context.Context, httpc *http.Client, simURL string) (Status, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(simURL, "/")+"/status", nil)
	if err != nil {
		return Status{}, err
	}
	resp, err := httpc.Do(req)
	if err != nil {
		return Status{}, fmt.Errorf("read sim status: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Status{}, fmt.Errorf("read sim status: HTTP %d", resp.StatusCode)
	}
	var s Status
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return Status{}, fmt.Errorf("decode sim status: %w", err)
	}
	return s, nil
}
