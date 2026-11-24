// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/userclient"
)

// The STRING metric phase.
//
// A device can report a state as text ("heating") under a metric declared STRING on its profile.
// The producer shipped after v1.9, and until this phase its upgrade behaviour was only unit-tested:
// nothing in the drill declared one, posted a text reading or read it back through the doors that
// serve it.
//
// 🔴 VERSION-GATED, AND THE GATE IS READ FROM THE TARGET'S OWN SERVED SCHEMAS, not from a flag the
// rig could get wrong. A target whose device-state LatestMeasurement and event-management
// MeasurementEvent both serve `textValue` is one that serves the producer, and the phase then
// proves the whole path. A v1.9 target serves neither, and there the phase asserts the BASELINE
// behaviour instead of skipping: declaring a STRING metric is REFUSED. A skip would make the phase
// green on a release it says nothing about.
const (
	// stringMetricKey is the key the probe declares STRING and reports text under.
	stringMetricKey = "mode"
	// stringMetricText is the reading the probe posts; the read-back compares it exactly.
	stringMetricText = "heating"
	// codeNotAggregatable is the extensions.code event-management answers when an aggregate names
	// a metric whose readings are text.
	codeNotAggregatable = "NOT_AGGREGATABLE"
)

// stringReadField is the field the phase reads a text reading's value from, on both read doors.
//
// 🔴 IT IS A VARIABLE FOR THE NEGATIVE CONTROL AND NOTHING ELSE. The planted defect is a probe that
// reads `value` instead: a text reading has a null `value`, so the phase must go red. `--control
// read-value` sets it, so a rig can show the phase failing against a healthy target.
var stringReadField = "textValue"

// Settle bounds for a posted reading to reach each store. Variables so tests can shrink them.
var (
	stringSettleTimeout  = 90 * time.Second
	stringSettleInterval = 2 * time.Second
)

const (
	createProfileMutation  = `mutation($req:DeviceProfileCreateRequest!){createDeviceProfile(request:$req){token}}`
	createMetricMutation   = `mutation($req:MetricDefinitionCreateRequest!){createMetricDefinition(request:$req){token dataType}}`
	publishProfileMutation = `mutation($token:String!,$label:String){publishDeviceProfile(token:$token,label:$label){version}}`
	createTypeMutation     = `mutation($req:DeviceTypeCreateRequest!){createDeviceType(request:$req){token}}`
	createDeviceMutation   = `mutation($req:DeviceCreateRequest!){createDevice(request:$req){token}}`
	createCredMutation     = `mutation($req:DeviceCredentialCreateRequest!){createDeviceCredential(request:$req){token}}`

	latestTextQuery    = `query($d:String!){latestMeasurements(deviceToken:$d){name value textValue}}`
	eventsTextQuery    = `query($c:EventSearchCriteria!){measurementEvents(criteria:$c){results{name value textValue}}}`
	aggregateTextQuery = `query($c:MeasurementAggregationCriteria!){bucketedMeasurements(criteria:$c){bucketStart name avg}}`
)

// producerServed reports whether the target's served schemas include the STRING producer, and why
// not. Both read doors must carry textValue: one without the other is a half-shipped producer, which
// is a finding rather than a version.
func producerServed(b *baseline) (bool, string, error) {
	ds := b.typeDeclaresField("device-state", "LatestMeasurement", "textValue")
	em := b.typeDeclaresField("event-management", "MeasurementEvent", "textValue")
	switch {
	case ds && em:
		return true, "", nil
	case !ds && !em:
		return false, "neither device-state LatestMeasurement nor event-management MeasurementEvent serves textValue", nil
	case ds:
		return false, "", failWith(exitShape, "device-state serves textValue but event-management MeasurementEvent does not: the producer is half shipped")
	default:
		return false, "", failWith(exitShape, "event-management serves textValue but device-state LatestMeasurement does not: the producer is half shipped")
	}
}

func runStringMetric(ctx context.Context, argv []string) error {
	return stringMetricTo(ctx, argv, os.Stdout)
}

func stringMetricTo(ctx context.Context, argv []string, stdout io.Writer) error {
	fs := flagSetFor("stringmetric")
	var c connection
	c.bind(fs)
	var receipt, schemas, eventsURL, control string
	fs.StringVar(&receipt, "receipt", "", "path to the receipt seed wrote (required)")
	fs.StringVar(&schemas, "schemas", "", "the DEPLOYED release's `backend/services` tree; decides whether the target serves the producer (required)")
	fs.StringVar(&eventsURL, "events-url", "http://localhost:8081", "event-sources HTTP ingress base (it stays inside the cluster; the rig port-forwards)")
	fs.StringVar(&control, "control", "", "NEGATIVE CONTROL: plant a defect so the phase must go red. One of: read-value")
	fs.DurationVar(&stringSettleTimeout, "settle", stringSettleTimeout, "how long a posted reading may take to be readable")
	if err := fs.Parse(argv); err != nil {
		return failWith(exitSetup, "%w", err)
	}
	if strings.TrimSpace(receipt) == "" || strings.TrimSpace(schemas) == "" {
		return failWith(exitSetup, "--receipt and --schemas are both required")
	}
	switch control {
	case "":
	case "read-value":
		stringReadField = "value"
		defer func() { stringReadField = "textValue" }()
	default:
		return failWith(exitSetup, "unknown --control %q (want read-value)", control)
	}
	rec, err := readReceipt(receipt)
	if err != nil {
		return err
	}
	c.tenant = rec.Tenant
	base, err := loadBaseline(schemas)
	if err != nil {
		return err
	}
	served, why, err := producerServed(base)
	if err != nil {
		return err
	}
	p := &stringProbe{
		c:         &c,
		session:   c.session(rec.Identity),
		instance:  rec.Instance,
		eventsURL: strings.TrimRight(eventsURL, "/"),
		httpc:     c.httpClient(),
		out:       stdout,
		suffix:    fmt.Sprintf("%x", time.Now().UnixNano()),
	}
	if !served {
		return p.expectRefused(ctx, why)
	}
	return p.expectServed(ctx)
}

type stringProbe struct {
	c         *connection
	session   querier
	instance  string
	eventsURL string
	httpc     *http.Client
	out       io.Writer
	suffix    string
}

func (p *stringProbe) tok(name string) string { return "apiprobe-str-" + p.suffix + "-" + name }

// probeDoc is one document the phase sends, with the variables it sends it with, so a test can
// validate every one of them against the served schemas.
type probeDoc struct {
	what, area, doc string
	vars            map[string]any
}

// setupDocs are the mutations that stand the probe's profile, metric, device and credential up, in
// order. The first two are the declaration; the rest are only sent to a target that serves the producer.
func (p *stringProbe) setupDocs() []probeDoc {
	return []probeDoc{
		{"create the string-metric profile", "device-management", createProfileMutation, map[string]any{"req": map[string]any{
			"token": p.tok("profile"), "name": "apiprobe string metric", "category": "probe"}}},
		{"declare the STRING metric", "device-management", createMetricMutation, map[string]any{"req": map[string]any{
			"token": p.tok("metric"), "deviceProfileToken": p.tok("profile"), "metricKey": stringMetricKey,
			"name": "Mode", "dataType": "STRING"}}},
		{"publish the profile", "device-management", publishProfileMutation, map[string]any{
			"token": p.tok("profile"), "label": "apiprobe-string"}},
		{"create the device type", "device-management", createTypeMutation, map[string]any{"req": map[string]any{
			"token": p.tok("type"), "name": "apiprobe string type", "profileToken": p.tok("profile")}}},
		{"create the device", "device-management", createDeviceMutation, map[string]any{"req": map[string]any{
			"token": p.tok("device"), "name": "apiprobe string device", "deviceTypeToken": p.tok("type")}}},
		{"create the device credential", "device-management", createCredMutation, map[string]any{"req": map[string]any{
			"token": p.tok("credential"), "deviceToken": p.tok("device"), "credentialType": "ACCESS_TOKEN",
			"credentialId": p.tok("credential-id"), "enabled": true}}},
	}
}

// readDocs are the reads, in the order the phase sends them.
func (p *stringProbe) readDocs() []probeDoc {
	return []probeDoc{
		{"latestMeasurements", "device-state", latestTextQuery, map[string]any{"d": p.tok("device")}},
		{"measurementEvents", "event-management", eventsTextQuery, map[string]any{"c": map[string]any{
			"deviceToken": p.tok("device"), "eventTypes": []any{2}, "pageNumber": 1, "pageSize": 25}}},
		{"the aggregate", "event-management", aggregateTextQuery, map[string]any{"c": map[string]any{
			"deviceToken": p.tok("device"), "name": stringMetricKey,
			"startTime": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339), "intervalSeconds": 60}}},
	}
}

// declareStringMetric creates a fresh profile and attempts to declare the STRING metric on it. The
// profile create is its own failure (exitRefused) and is not part of the verdict.
func (p *stringProbe) declareStringMetric(ctx context.Context) (profileErr, metricErr error) {
	docs := p.setupDocs()
	if err := p.session.Query(ctx, p.c.areaURL(docs[0].area), docs[0].doc, docs[0].vars, nil); err != nil {
		return failWith(exitRefused, "%s: %w", docs[0].what, err), nil
	}
	return nil, p.session.Query(ctx, p.c.areaURL(docs[1].area), docs[1].doc, docs[1].vars, nil)
}

// expectRefused is the v1.9 half: the baseline refuses to declare a STRING metric. It must be the
// platform ANSWERING with a refusal; a transport failure or a login failure proves nothing.
func (p *stringProbe) expectRefused(ctx context.Context, why string) error {
	profileErr, err := p.declareStringMetric(ctx)
	if profileErr != nil {
		return profileErr
	}
	if err == nil {
		return failWith(exitMismatch, "the target serves no STRING producer (%s) yet declaring a STRING metric was ACCEPTED", why)
	}
	var gqlErr *userclient.GraphQLError
	if !errors.As(err, &gqlErr) {
		return failWith(exitSetup, "declaring a STRING metric failed without a refusal from the platform: %w", err)
	}
	fmt.Fprintf(p.out, "stringmetric: target serves no STRING producer (%s); declaring a STRING metric was refused as expected: %s\n",
		why, strings.Join(gqlErr.Messages, "; "))
	return nil
}

func (p *stringProbe) expectServed(ctx context.Context) error {
	profileErr, err := p.declareStringMetric(ctx)
	if profileErr != nil {
		return profileErr
	}
	if err != nil {
		return failWith(exitRefused, "the target serves the STRING producer but declaring a STRING metric failed: %w", err)
	}
	for _, d := range p.setupDocs()[2:] {
		if err := p.session.Query(ctx, p.c.areaURL(d.area), d.doc, d.vars, nil); err != nil {
			return failWith(exitRefused, "%s: %w", d.what, err)
		}
	}
	credID := p.tok("credential-id")
	occurred := time.Now().UTC().Format(time.RFC3339)
	if err := p.post(ctx, credID, occurred); err != nil {
		return err
	}
	if err := p.checkLatest(ctx); err != nil {
		return err
	}
	if err := p.checkEvents(ctx); err != nil {
		return err
	}
	if err := p.checkAggregateRefused(ctx); err != nil {
		return err
	}
	fmt.Fprintf(p.out, "stringmetric: STRING metric %q served: text %q read back as %s through latestMeasurements and measurementEvents (value null); an aggregate over it was refused\n",
		stringMetricKey, stringMetricText, stringReadField)
	return nil
}

func (p *stringProbe) post(ctx context.Context, credID, occurred string) error {
	body, err := json.Marshal(map[string]any{
		"device":         p.tok("device"),
		"eventType":      "Measurement",
		"occurredTime":   occurred,
		"credentialType": "ACCESS_TOKEN",
		"credentialId":   credID,
		"payload": map[string]any{"entries": []any{map[string]any{
			"measurements": map[string]string{stringMetricKey: stringMetricText},
			"occurredTime": occurred,
		}}},
	})
	if err != nil {
		return failWith(exitSetup, "marshal the event: %w", err)
	}
	url := fmt.Sprintf("%s/%s/%s/events", p.eventsURL, p.instance, p.c.tenant)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return failWith(exitSetup, "build the ingress request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.httpc.Do(req)
	if err != nil {
		return failWith(exitSetup, "post the text reading to %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return failWith(exitRefused, "the ingress refused the text reading with %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return nil
}

// reading is one row of either read door.
type reading struct {
	Name      string   `json:"name"`
	Value     *float64 `json:"value"`
	TextValue *string  `json:"textValue"`
}

// readField is the value the phase reads, chosen by stringReadField. A null reads as "", which is
// never the text.
func (r reading) readField() string {
	if stringReadField == "value" {
		if r.Value == nil {
			return ""
		}
		return fmt.Sprint(*r.Value)
	}
	if r.TextValue == nil {
		return ""
	}
	return *r.TextValue
}

// verdict is nil when the row is the text reading: the text under textValue and a NULL value.
func (r reading) verdict(door string) error {
	if r.readField() != stringMetricText {
		return failWith(exitMismatch, "%s: %q read back as %s=%q, want %q (value=%v)",
			door, r.Name, stringReadField, r.readField(), stringMetricText, ptrFloat(r.Value))
	}
	if r.Value != nil {
		return failWith(exitMismatch, "%s: the text reading %q also carries a numeric value %v; a reading has exactly one of value and textValue",
			door, r.Name, *r.Value)
	}
	return nil
}

func ptrFloat(f *float64) any {
	if f == nil {
		return nil
	}
	return *f
}

// await polls fn until it reports done, fails, or the settle bound passes.
func (p *stringProbe) await(ctx context.Context, what string, fn func() (bool, error)) error {
	deadline := time.Now().Add(stringSettleTimeout)
	for {
		done, err := fn()
		if err != nil {
			return err
		}
		if done {
			return nil
		}
		if !time.Now().Before(deadline) {
			return failWith(exitMissing, "%s: the text reading never appeared within %s", what, stringSettleTimeout)
		}
		select {
		case <-ctx.Done():
			return failWith(exitSetup, "waiting for %s: %w", what, ctx.Err())
		case <-time.After(stringSettleInterval):
		}
	}
}

func (p *stringProbe) checkLatest(ctx context.Context) error {
	rd := p.readDocs()[0]
	return p.await(ctx, "latestMeasurements", func() (bool, error) {
		var out struct {
			Rows []reading `json:"latestMeasurements"`
		}
		if err := p.session.Query(ctx, p.c.areaURL(rd.area), rd.doc, rd.vars, &out); err != nil {
			return false, failWith(exitShape, "latestMeasurements: %w", err)
		}
		for _, r := range out.Rows {
			if r.Name == stringMetricKey {
				return true, r.verdict("latestMeasurements")
			}
		}
		return false, nil
	})
}

func (p *stringProbe) checkEvents(ctx context.Context) error {
	rd := p.readDocs()[1]
	return p.await(ctx, "measurementEvents", func() (bool, error) {
		var out struct {
			Page struct {
				Results []reading `json:"results"`
			} `json:"measurementEvents"`
		}
		if err := p.session.Query(ctx, p.c.areaURL(rd.area), rd.doc, rd.vars, &out); err != nil {
			return false, failWith(exitShape, "measurementEvents: %w", err)
		}
		for _, r := range out.Page.Results {
			if r.Name == stringMetricKey {
				return true, r.verdict("measurementEvents")
			}
		}
		return false, nil
	})
}

// checkAggregateRefused asks for an aggregate over the text metric and requires the platform's own
// refusal. An answer, even an empty one, is a defect: a text reading must never be averaged.
func (p *stringProbe) checkAggregateRefused(ctx context.Context) error {
	rd := p.readDocs()[2]
	var out struct {
		Buckets []json.RawMessage `json:"bucketedMeasurements"`
	}
	err := p.session.Query(ctx, p.c.areaURL(rd.area), rd.doc, rd.vars, &out)
	if err == nil {
		return failWith(exitMismatch, "an aggregate over the STRING metric %q was ANSWERED (%d bucket(s)); it must be refused with %s",
			stringMetricKey, len(out.Buckets), codeNotAggregatable)
	}
	if !userclient.AllHaveCode(err, codeNotAggregatable) {
		return failWith(exitMismatch, "an aggregate over the STRING metric %q was refused, but not with %s: %w",
			stringMetricKey, codeNotAggregatable, err)
	}
	return nil
}
