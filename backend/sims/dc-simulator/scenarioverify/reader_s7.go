// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package scenarioverify

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"time"

	"github.com/devicechain-io/dc-simulator/sim"
)

const queryS7Devices = `query($c:DeviceSearchCriteria!){devices(criteria:$c){results{id token externalId description} pagination{totalRecords}}}`
const queryS7Alarms = `query($c:AlarmSearchCriteria!){alarms(criteria:$c){results{token originatorId originatorToken alarmKey state severity} pagination{totalRecords}}}`
const queryS7Events = `query($c:EventSearchCriteria!){measurementEvents(criteria:$c){results{id deviceToken name value} pagination{totalRecords}}}`
const queryS7Buckets = `query($c:MeasurementAggregationCriteria!){bucketedMeasurements(criteria:$c){bucketStart name avg min max sum count}}`
const queryS7States = `query($tokens:[String!]!){deviceStatesByDeviceToken(deviceTokens:$tokens){deviceToken active presenceSource lastConnectTime lastDisconnectTime lastActivityTime inactivityTimeout}}`
const queryS7Commands = `query($c:CommandSearchCriteria!){commands(criteria:$c){results{token deviceToken name status queuedTime respondedTime} pagination{totalRecords}}}`

type S7Config struct {
	Peer       *sim.Handshake
	PeerSimURL string
	MCPURL     string
	ClientID   string
}

type s7Bucket struct {
	BucketStart string
	Name        string
	Avg         *float64
	Min         *float64
	Max         *float64
	Sum         *float64
	Count       int
}

type s7State struct {
	DeviceToken        string
	Active             bool
	PresenceSource     string
	LastConnectTime    string
	LastDisconnectTime string
	LastActivityTime   string
	InactivityTimeout  int
}

type s7Command struct {
	Token         string
	DeviceToken   string
	Name          string
	Status        string
	QueuedTime    string
	RespondedTime string
}

func s7JSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func s7Decode[T any](v any) (T, error) {
	var out T
	b, err := json.Marshal(v)
	if err == nil {
		err = json.Unmarshal(b, &out)
	}
	return out, err
}

// Enumerate every page, requiring a stable total; a truncated read is no verdict.
func s7Rows[T any](ctx context.Context, r *Reader, endpoint, doc, field string, filters map[string]any) ([]T, error) {
	rows := []T{}
	criteria := map[string]any{}
	for k, v := range filters {
		criteria[k] = v
	}
	total := -1
	for page := 1; page <= maxPages; page++ {
		criteria["pageNumber"], criteria["pageSize"] = page, 100
		var response map[string]struct {
			Results    []T
			Pagination struct{ TotalRecords *int }
		}
		if err := r.session.Query(ctx, endpoint, doc, map[string]any{"c": criteria}, &response); err != nil {
			return nil, fmt.Errorf("S7 %s platform read failed", field)
		}
		data, ok := response[field]
		if !ok || data.Pagination.TotalRecords == nil {
			return nil, fmt.Errorf("S7 %s omitted pagination", field)
		}
		if total < 0 {
			total = *data.Pagination.TotalRecords
		}
		if total != *data.Pagination.TotalRecords {
			return nil, fmt.Errorf("S7 %s changed during pagination", field)
		}
		rows = append(rows, data.Results...)
		if len(rows) == total {
			return rows, nil
		}
		if len(rows) > total || len(data.Results) == 0 {
			return nil, fmt.Errorf("S7 %s incomplete pagination", field)
		}
	}
	return nil, fmt.Errorf("S7 %s exceeded page bound", field)
}

func s7ToolRows[T any](ctx context.Context, m *s7MCP, tool, field string, filters map[string]any) ([]T, error) {
	rows := []T{}
	args := map[string]any{}
	for k, v := range filters {
		args[k] = v
	}
	total := -1
	for page := 1; page <= maxPages; page++ {
		args["pageNumber"], args["pageSize"] = page, 100
		out, err := m.call(ctx, tool, args)
		if err != nil {
			return nil, err
		}
		n, ok := out["totalRecords"].(float64)
		if !ok || n < 0 || n != float64(int(n)) {
			return nil, fmt.Errorf("MCP %s omitted pagination", tool)
		}
		if total < 0 {
			total = int(n)
		}
		batch, err := s7Decode[[]T](out[field])
		if err != nil || int(n) != total {
			return nil, fmt.Errorf("MCP %s changed or malformed pagination", tool)
		}
		rows = append(rows, batch...)
		if len(rows) == total {
			return rows, nil
		}
		if len(rows) > total || len(batch) == 0 {
			return nil, fmt.Errorf("MCP %s incomplete pagination", tool)
		}
	}
	return nil, fmt.Errorf("MCP %s exceeded page bound", tool)
}

func s7Alarms(ctx context.Context, r *Reader) ([]S7Alarm, error) {
	rows, err := s7Rows[S7Alarm](ctx, r, r.endpoints.DeviceMgmtGraphQL, queryS7Alarms, "alarms", nil)
	sort.Slice(rows, func(i, j int) bool { return rows[i].Token < rows[j].Token })
	return rows, err
}

func s7Truth(ctx context.Context, r *Reader, from, to time.Time) (S7Truth, error) {
	var truth S7Truth
	var err error
	truth.Devices, err = s7Rows[S7Device](ctx, r, r.endpoints.DeviceMgmtGraphQL, queryS7Devices, "devices", nil)
	if err != nil {
		return truth, err
	}
	sort.Slice(truth.Devices, func(i, j int) bool { return truth.Devices[i].Token < truth.Devices[j].Token })
	truth.Alarms, err = s7Alarms(ctx, r)
	if err != nil {
		return truth, err
	}
	truth.Events, err = s7Rows[S7Event](ctx, r, r.events, queryS7Events, "measurementEvents",
		map[string]any{"startTime": from.Format(time.RFC3339Nano), "endTime": to.Format(time.RFC3339Nano)})
	sort.Slice(truth.Events, func(i, j int) bool { return truth.Events[i].ID < truth.Events[j].ID })
	return truth, err
}

func s7Inventory(devices []S7Device) []S7Device {
	out := append([]S7Device{}, devices...)
	for i := range out {
		out[i].ID = ""
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Token < out[j].Token })
	return out
}

func s7Flooding(alarms []S7Alarm) []string {
	quays := map[string]bool{}
	for _, a := range alarms {
		if a.AlarmKey != sim.HarborpulseFloodAlarmKey || a.State != sim.AlarmStateActiveWire || a.Severity != sim.AlarmSeverityCriticalWire {
			continue
		}
		for _, quay := range []string{sim.HarborpulseNorthQuay, sim.HarborpulseEastQuay, sim.HarborpulseBasin} {
			if slices.Contains(fleetOf(sim.HarborpulseGaugeType, quay), a.OriginatorToken) {
				quays[quay] = true
			}
		}
	}
	out := []string{}
	for quay := range quays {
		out = append(out, quay)
	}
	sort.Strings(out)
	return out
}

// Retry a moving alarm snapshot rather than turn an ordinary tier transition red.
func s7AlarmComparison(ctx context.Context, r *Reader, m *s7MCP) (S7Comparison, []S7Alarm, []S7Alarm, error) {
	for range 4 {
		before, err := s7Alarms(ctx, r)
		if err != nil {
			return S7Comparison{}, nil, nil, err
		}
		actual, err := s7ToolRows[S7Alarm](ctx, m, "list_alarms", "alarms", nil)
		if err != nil {
			return S7Comparison{}, nil, nil, err
		}
		sort.Slice(actual, func(i, j int) bool { return actual[i].Token < actual[j].Token })
		after, err := s7Alarms(ctx, r)
		if err != nil {
			return S7Comparison{}, nil, nil, err
		}
		if reflect.DeepEqual(before, after) {
			return S7Comparison{Expected: s7JSON(before), Actual: s7JSON(actual)}, before, actual, nil
		}
	}
	return S7Comparison{}, nil, nil, fmt.Errorf("S7 alarm snapshot did not settle")
}

func s7Aggregate(ctx context.Context, r *Reader, m *s7MCP, device, name string, from, to time.Time) (S7Comparison, []s7Bucket, []s7Bucket, error) {
	filters := map[string]any{"deviceToken": device, "name": name, "startTime": from.Format(time.RFC3339Nano), "endTime": to.Format(time.RFC3339Nano), "intervalSeconds": 3600}
	var truth struct {
		Buckets []s7Bucket `json:"bucketedMeasurements"`
	}
	if err := r.session.Query(ctx, r.events, queryS7Buckets, map[string]any{"c": filters}, &truth); err != nil {
		return S7Comparison{}, nil, nil, fmt.Errorf("S7 aggregate platform read failed")
	}
	out, err := m.call(ctx, "aggregate_measurements", filters)
	if err != nil {
		return S7Comparison{}, nil, nil, err
	}
	actual, err := s7Decode[[]s7Bucket](out["buckets"])
	if err != nil {
		return S7Comparison{}, nil, nil, fmt.Errorf("MCP aggregate decoding failed")
	}
	sort.Slice(truth.Buckets, func(i, j int) bool { return truth.Buckets[i].BucketStart < truth.Buckets[j].BucketStart })
	sort.Slice(actual, func(i, j int) bool { return actual[i].BucketStart < actual[j].BucketStart })
	return S7Comparison{Expected: s7JSON(truth.Buckets), Actual: s7JSON(actual)}, truth.Buckets, actual, nil
}

func s7StateComparison(ctx context.Context, r *Reader, other *Reader, m *s7MCP, tokens []string) (S7Comparison, error) {
	read := func() ([]s7State, error) {
		var out struct {
			States []s7State `json:"deviceStatesByDeviceToken"`
		}
		err := r.session.Query(ctx, r.endpoints.DeviceStateGraphQL, queryS7States, map[string]any{"tokens": tokens}, &out)
		sort.Slice(out.States, func(i, j int) bool { return out.States[i].DeviceToken < out.States[j].DeviceToken })
		return out.States, err
	}
	before, err := read()
	if err != nil {
		return S7Comparison{}, fmt.Errorf("S7 state platform read failed")
	}
	var foreign struct {
		States []s7State `json:"deviceStatesByDeviceToken"`
	}
	if err := other.session.Query(ctx, other.endpoints.DeviceStateGraphQL, queryS7States, map[string]any{"tokens": tokens}, &foreign); err != nil {
		return S7Comparison{}, fmt.Errorf("S7 peer state platform read failed")
	}
	if !s7StateWitness(before, foreign.States, tokens) {
		return S7Comparison{Expected: s7JSON("complete distinct tenant B broker admissions"), Actual: s7JSON(before)}, nil
	}
	out, err := m.call(ctx, "get_device_state", map[string]any{"deviceTokens": tokens})
	if err != nil {
		return S7Comparison{}, err
	}
	actual, err := s7Decode[[]s7State](out["states"])
	if err != nil {
		return S7Comparison{}, fmt.Errorf("MCP state decoding failed")
	}
	after, err := read()
	if err != nil {
		return S7Comparison{}, fmt.Errorf("S7 state platform reread failed")
	}
	sort.Slice(actual, func(i, j int) bool { return actual[i].DeviceToken < actual[j].DeviceToken })
	if len(before) != len(tokens) || len(after) != len(tokens) || len(actual) != len(tokens) {
		return S7Comparison{Expected: s7JSON("complete tenant B states"), Actual: s7JSON(actual)}, nil
	}
	for i := range before {
		lo, e1 := time.Parse(time.RFC3339Nano, before[i].LastActivityTime)
		hi, e2 := time.Parse(time.RFC3339Nano, after[i].LastActivityTime)
		at, e3 := time.Parse(time.RFC3339Nano, actual[i].LastActivityTime)
		if e1 != nil || e2 != nil || e3 != nil || at.Before(lo) || at.After(hi) {
			return S7Comparison{Expected: s7JSON("valid tenant B activity within GraphQL bounds"), Actual: s7JSON(actual)}, nil
		}
		// Keep only the stable columns after validating activity against its two
		// independently read platform bounds.
		before[i].LastActivityTime, after[i].LastActivityTime, actual[i].LastActivityTime = "", "", ""
	}
	if !reflect.DeepEqual(before, after) {
		return S7Comparison{}, fmt.Errorf("S7 connectivity snapshot moved")
	}
	return S7Comparison{Expected: s7JSON(before), Actual: s7JSON(actual)}, nil
}

func s7StateWitness(own, foreign []s7State, tokens []string) bool {
	if len(own) != len(tokens) || len(foreign) != len(tokens) {
		return false
	}
	other := map[string]s7State{}
	for _, row := range foreign {
		if _, exists := other[row.DeviceToken]; exists {
			return false
		}
		other[row.DeviceToken] = row
	}
	seen := map[string]bool{}
	for _, row := range own {
		peer, ok := other[row.DeviceToken]
		if !ok || seen[row.DeviceToken] || !slices.Contains(tokens, row.DeviceToken) || row.PresenceSource != "ASSERTED" || peer.PresenceSource != "ASSERTED" || row.LastConnectTime == "" || peer.LastConnectTime == "" || row.LastConnectTime == peer.LastConnectTime {
			return false
		}
		ownAt, ownErr := time.Parse(time.RFC3339Nano, row.LastConnectTime)
		peerAt, peerErr := time.Parse(time.RFC3339Nano, peer.LastConnectTime)
		if ownErr != nil || peerErr != nil || ownAt.Equal(peerAt) {
			return false
		}
		seen[row.DeviceToken] = true
	}
	return true
}

// collectS7 runs beside the alarm watch: MCP calls cannot introduce a poll gap
// into any S1-S6 invariant. The peer is a real second harborpulse sim and member.
func collectS7(ctx context.Context, cfg Config, reader *Reader, defects []string) (*S7, error) {
	c := cfg.S7
	if c.Peer == nil || c.Peer.Tenant == cfg.Handshake.Tenant || c.Peer.InstanceId != cfg.Handshake.InstanceId || c.Peer.Endpoints.DeviceMgmtGraphQL != cfg.Handshake.Endpoints.DeviceMgmtGraphQL || c.PeerSimURL == "" || c.MCPURL == "" || c.ClientID == "" {
		return nil, fmt.Errorf("S7 requires a distinct peer handshake, peer sim, MCP endpoint and per-run client")
	}
	peer, err := NewReader(c.Peer, cfg.HTTP)
	if err != nil {
		return nil, err
	}
	clock, _, err := awaitStart(ctx, Config{Handshake: c.Peer, SimURL: c.PeerSimURL, StatusHTTP: cfg.StatusHTTP, StartTimeout: cfg.StartTimeout})
	if err != nil {
		return nil, err
	}
	day := clock.TargetDay(time.Now(), 19.5)
	if err := sleepUntil(ctx, clock.At(day, 19.5)); err != nil {
		return nil, err
	}
	// Ask during the storm peak, after pump commands have had time to act.
	// Platform critical alarms, never simulator counters, admit the question.
	for time.Now().Before(clock.At(day, 21)) {
		alarms, err := s7Alarms(ctx, peer)
		if err != nil {
			return nil, err
		}
		if len(s7Flooding(alarms)) > 0 {
			break
		}
		if err := sleepUntil(ctx, time.Now().Add(2*time.Second)); err != nil {
			return nil, err
		}
	}
	resource, err := s7Resource(ctx, cfg.HTTP, c.MCPURL)
	if err != nil {
		return nil, err
	}
	token, err := s7Authorize(ctx, cfg.HTTP, c.Peer, c.ClientID, resource)
	if err != nil {
		return nil, err
	}
	m := &s7MCP{http: cfg.HTTP, endpoint: c.MCPURL, token: token}
	if err := m.initialize(ctx); err != nil {
		return nil, err
	}
	defer m.close()
	s := &S7{TenantA: reader.tenant, TenantB: peer.tenant, IsolationReads: map[string]S7Comparison{}, QAReads: map[string]S7Comparison{}}
	alarmRead, alarms, answers, err := s7AlarmComparison(ctx, peer, m)
	if err != nil {
		return nil, err
	}
	s.IsolationReads["list_alarms"], s.QAReads["list_alarms"] = alarmRead, alarmRead
	s.ExpectedFlooding, s.Flooding = s7Flooding(alarms), s7Flooding(answers)
	end := time.Now().Add(-30 * time.Second).UTC()
	start := end.Add(-90 * time.Second)
	s.A, err = s7Truth(ctx, reader, start, end)
	if err != nil {
		return nil, err
	}
	s.B, err = s7Truth(ctx, peer, start, end)
	if err != nil {
		return nil, err
	}
	s.ObservedA = s.A
	if slices.Contains(defects, sim.HarborpulseDefectIsolationCrossRead) {
		// Planted caller confusion: actually read B through B's identity in the
		// slot where A's identity belongs. Q&A still uses the untouched B session.
		s.ObservedA, err = s7Truth(ctx, peer, start, end)
		if err != nil {
			return nil, err
		}
	}
	devices, err := s7ToolRows[S7Device](ctx, m, "list_devices", "devices", nil)
	if err != nil {
		return nil, err
	}
	s.IsolationReads["list_devices"] = S7Comparison{Expected: s7JSON(s7Inventory(s.B.Devices)), Actual: s7JSON(s7Inventory(devices))}
	pumps := fleetOf(sim.HarborpulsePumpType, "")
	// Asserted broker admissions distinguish the two tenants even when their
	// identical pumps happen to report the same running value at this instant.
	tokens := sim.HarborpulseMQTTChargers()
	s.IsolationReads["get_device_state"], err = s7StateComparison(ctx, peer, reader, m, tokens)
	if err != nil {
		return nil, err
	}
	marker, buckets, _, err := s7Aggregate(ctx, peer, m, pumps[0], sim.HarborpulseTenantMarkerKey, start, end)
	if err != nil {
		return nil, err
	}
	markerOK := len(buckets) > 0
	for _, b := range buckets {
		markerOK = markerOK && b.Count > 0 && b.Min != nil && b.Max != nil && *b.Min == sim.HarborpulseTenantMarker(peer.tenant) && *b.Max == sim.HarborpulseTenantMarker(peer.tenant)
	}
	if !markerOK {
		marker.Expected = s7JSON("tenant B's stored measurement witness is missing")
	}
	s.IsolationReads["aggregate_measurements"] = marker
	s.Pumps, s.ExpectedPumps = []string{}, []string{}
	truthAggregates, toolAggregates := map[string][]s7Bucket{}, map[string][]s7Bucket{}
	truthCommands, toolCommands := []s7Command{}, []s7Command{}
	for _, device := range pumps {
		_, truth, actual, err := s7Aggregate(ctx, peer, m, device, sim.HarborpulsePumpRunningKey, clock.At(day, 17), end)
		if err != nil {
			return nil, err
		}
		truthAggregates[device], toolAggregates[device] = truth, actual
		for _, answer := range []struct {
			rows  []s7Bucket
			pumps *[]string
		}{{truth, &s.ExpectedPumps}, {actual, &s.Pumps}} {
			for _, b := range answer.rows {
				if b.Count > 0 && b.Max != nil && *b.Max > 0.5 {
					*answer.pumps = append(*answer.pumps, device)
					break
				}
			}
		}
		expected, err := s7Rows[s7Command](ctx, peer, peer.endpoints.CommandMgmtGraphQL, queryS7Commands, "commands", map[string]any{"deviceToken": device, "status": "SUCCESSFUL"})
		if err != nil {
			return nil, err
		}
		returned, err := s7ToolRows[s7Command](ctx, m, "list_commands", "commands", map[string]any{"deviceToken": device, "status": "SUCCESSFUL"})
		if err != nil {
			return nil, err
		}
		filter := func(rows []s7Command) []s7Command {
			out := []s7Command{}
			for _, command := range rows {
				at, err := time.Parse(time.RFC3339Nano, command.QueuedTime)
				if err == nil && !at.Before(clock.At(day, 17)) && !at.After(end) {
					out = append(out, command)
				}
			}
			return out
		}
		truthCommands, toolCommands = append(truthCommands, filter(expected)...), append(toolCommands, filter(returned)...)
	}
	sort.Slice(truthCommands, func(i, j int) bool { return truthCommands[i].Token < truthCommands[j].Token })
	sort.Slice(toolCommands, func(i, j int) bool { return toolCommands[i].Token < toolCommands[j].Token })
	s.QAReads["list_commands"] = S7Comparison{Expected: s7JSON(truthCommands), Actual: s7JSON(toolCommands)}
	s.QAReads["aggregate_measurements"] = S7Comparison{Expected: s7JSON(truthAggregates), Actual: s7JSON(toolAggregates)}
	sort.Strings(s.Pumps)
	sort.Strings(s.ExpectedPumps)
	if slices.Contains(defects, sim.HarborpulseDefectMCPWrongAnswer) {
		s.Flooding = []string{sim.HarborpulseNorthQuay}
	}
	return s, nil
}
