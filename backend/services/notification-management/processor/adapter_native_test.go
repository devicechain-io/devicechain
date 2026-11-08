// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/egress"
	"github.com/devicechain-io/dc-microservice/httpsink"
	"github.com/devicechain-io/dc-notification-management/model"
)

// providerFake is an httptest stand-in for a vendor API: it records the request and answers
// with a configured status and body.
type providerFake struct {
	srv    *httptest.Server
	status int
	reply  string
	body   map[string]any
	header http.Header
	path   string
	calls  int
}

func newProviderFake(t *testing.T, status int, reply string) *providerFake {
	t.Helper()
	f := &providerFake{status: status, reply: reply}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls++
		f.header = r.Header.Clone()
		f.path = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		f.body = nil
		_ = json.Unmarshal(raw, &f.body)
		w.WriteHeader(f.status)
		_, _ = io.WriteString(w, f.reply)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func facts(eventType string) AlarmFacts {
	v := 91.5
	return AlarmFacts{
		EventType: eventType, AlarmToken: "alarm-7", AlarmKey: "overheat", Severity: "CRITICAL",
		State: "ACTIVE", Originator: "device 42", MetricKey: "temp", Value: &v,
		RaisedTime:   time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC),
		OccurredTime: time.Date(2026, 11, 6, 10, 1, 0, 0, time.UTC),
	}
}

func rendered(eventType string) *RenderedNotification {
	return &RenderedNotification{Subject: "[CRITICAL] Alarm: overheat", Alarm: facts(eventType)}
}

// asMaps digs a JSON array of objects out of a decoded document.
func asMaps(t *testing.T, v any) []map[string]any {
	t.Helper()
	arr, ok := v.([]any)
	if !ok {
		t.Fatalf("not an array: %#v", v)
	}
	out := make([]map[string]any, len(arr))
	for i, e := range arr {
		out[i], ok = e.(map[string]any)
		if !ok {
			t.Fatalf("element %d not an object: %#v", i, e)
		}
	}
	return out
}

func slackBlocks(t *testing.T, body map[string]any) (string, []map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(body)
	body = nil
	_ = json.Unmarshal(raw, &body)
	att := asMaps(t, body["attachments"])
	if len(att) != 1 {
		t.Fatalf("want one attachment, got %d", len(att))
	}
	return att[0]["color"].(string), asMaps(t, att[0]["blocks"])
}

func blockTypes(blocks []map[string]any) []string {
	out := make([]string, len(blocks))
	for i, b := range blocks {
		out[i] = b["type"].(string)
	}
	return out
}

func TestSlackPostsBlockKitColouredBySeverityWithLinkAndAckHint(t *testing.T) {
	f := newProviderFake(t, 200, "ok")
	a := &slackAdapter{client: f.srv.Client()}
	ch := channelWith("slack-1", model.ChannelTypeSlack, `{"linkBaseUrl":"https://console.example.com/"}`)

	if err := a.Deliver(context.Background(), ch, f.srv.URL+"/services/T/B/x", []string{"ignored"}, rendered("RAISED")); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if f.path != "/services/T/B/x" {
		t.Fatalf("posted to %q: the secret URL must be the destination", f.path)
	}
	if f.header.Get("Authorization") != "" {
		t.Fatalf("a Slack webhook must carry no Authorization header, got %q", f.header.Get("Authorization"))
	}
	if f.body["text"] != "[CRITICAL] Alarm: overheat" {
		t.Fatalf("fallback text = %v", f.body["text"])
	}
	color, blocks := slackBlocks(t, f.body)
	if color != "#B71C1C" {
		t.Fatalf("CRITICAL colour = %q", color)
	}
	if got := strings.Join(blockTypes(blocks), ","); got != "header,section,context,actions" {
		t.Fatalf("blocks = %s", got)
	}
	if !strings.Contains(blocks[2]["elements"].([]any)[0].(map[string]any)["text"].(string), "Acknowledge") {
		t.Fatalf("open alarm must carry the acknowledge hint: %v", blocks[2])
	}
	btn := blocks[3]["elements"].([]any)[0].(map[string]any)
	if btn["url"] != "https://console.example.com/alarms" {
		t.Fatalf("link = %v (trailing slash must not double)", btn["url"])
	}
	fields := asMaps(t, blocks[1]["fields"])
	var flat []string
	for _, fl := range fields {
		flat = append(flat, fl["text"].(string))
	}
	joined := strings.Join(flat, "|")
	for _, want := range []string{"*Severity*\nCRITICAL", "*Source*\ndevice 42", "*Metric*\ntemp", "*Value*\n91.5"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("fields missing %q in %q", want, joined)
		}
	}
}

func TestSlackColourFollowsSeverityAndLifecycle(t *testing.T) {
	cases := []struct {
		event, sev, want string
	}{
		{"RAISED", "CRITICAL", "#B71C1C"},
		{"RAISED", "MAJOR", "#E65100"},
		{"RAISED", "MINOR", "#F9A825"},
		{"RAISED", "WARNING", "#FBC02D"},
		{"RAISED", "INDETERMINATE", "#757575"},
		{"CLEARED", "CRITICAL", "#2E7D32"},
		{"ACKNOWLEDGED", "CRITICAL", "#1565C0"},
	}
	for _, c := range cases {
		a := AlarmFacts{EventType: c.event, Severity: c.sev}
		if got := severityColor(a); got != c.want {
			t.Errorf("%s/%s colour = %q, want %q", c.event, c.sev, got, c.want)
		}
	}
}

func TestSlackOmitsAckHintOnceHandledAndLinkWhenUnconfigured(t *testing.T) {
	f := newProviderFake(t, 200, "ok")
	a := &slackAdapter{client: f.srv.Client()}
	ch := channelWith("slack-1", model.ChannelTypeSlack, "")
	for _, ev := range []string{"CLEARED", "ACKNOWLEDGED"} {
		if err := a.Deliver(context.Background(), ch, f.srv.URL+"/h", nil, rendered(ev)); err != nil {
			t.Fatalf("deliver %s: %v", ev, err)
		}
		_, blocks := slackBlocks(t, f.body)
		if got := strings.Join(blockTypes(blocks), ","); got != "header,section" {
			t.Fatalf("%s blocks = %s, want header,section (no ack hint, no link)", ev, got)
		}
	}
	// An acknowledged alarm that is re-announced for another reason is also not "open".
	r := rendered("ESCALATED")
	r.Alarm.Acknowledged = true
	if r.Alarm.open() {
		t.Fatal("an acknowledged alarm must not read as open")
	}
}

func TestSlackEscapesTenantAuthoredText(t *testing.T) {
	if got := slackEscape("<!channel> a&b"); got != "&lt;!channel&gt; a&amp;b" {
		t.Fatalf("escape = %q", got)
	}
	r := rendered("RAISED")
	r.Alarm.MetricKey = "<!here>"
	msg := slackMessage(r, "")
	raw, _ := json.Marshal(msg)
	if strings.Contains(string(raw), "<!here>") {
		t.Fatalf("an unescaped mention reached the payload: %s", raw)
	}
}

func TestSlackTruncatesLongHeader(t *testing.T) {
	r := rendered("RAISED")
	r.Alarm.AlarmKey = strings.Repeat("k", 400)
	_, blocks := slackBlocks(t, slackMessage(r, ""))
	h := blocks[0]["text"].(map[string]any)["text"].(string)
	if n := len([]rune(h)); n != 150 {
		t.Fatalf("header length = %d, want exactly 150 (Slack rejects longer)", n)
	}
}

func TestSlackErrorNeverContainsTheWebhookURL(t *testing.T) {
	f := newProviderFake(t, 500, "boom")
	secretURL := f.srv.URL + "/services/T000/B000/SUPERSECRET"
	a := &slackAdapter{client: f.srv.Client()}
	err := a.Deliver(context.Background(), channelWith("s", model.ChannelTypeSlack, ""), secretURL, nil, rendered("RAISED"))
	if err == nil {
		t.Fatal("a 500 must fail the delivery")
	}
	if strings.Contains(err.Error(), "SUPERSECRET") {
		t.Fatalf("the secret webhook path leaked into the error: %v", err)
	}
	// A transport failure is where net/http quotes the URL.
	f.srv.Close()
	err = a.Deliver(context.Background(), channelWith("s", model.ChannelTypeSlack, ""), secretURL, nil, rendered("RAISED"))
	if err == nil || strings.Contains(err.Error(), "SUPERSECRET") {
		t.Fatalf("transport error leaked the secret URL (or succeeded): %v", err)
	}
}

func TestScrubSecretKeepsTheErrorChain(t *testing.T) {
	cause := &egress.BlockedError{Reason: "private"}
	err := scrubSecret(errors.New("dial https://h/SECRET: "+cause.Error()), "https://h/SECRET")
	if strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("not scrubbed: %v", err)
	}
	wrapped := scrubSecret(errorsJoin("POST https://h/SECRET", cause), "https://h/SECRET")
	if !errors.Is(wrapped, egress.ErrBlocked) {
		t.Fatalf("scrubbing must keep the egress classification reachable: %v", wrapped)
	}
	if scrubSecret(nil, "x") != nil {
		t.Fatal("nil must pass through")
	}
	same := errors.New("clean")
	if scrubSecret(same, "") != same || scrubSecret(same, "absent") != same {
		t.Fatal("an error without the secret must be returned untouched")
	}
}

func errorsJoin(msg string, cause error) error {
	return &wrapErr{msg: msg + ": " + cause.Error(), cause: cause}
}

type wrapErr struct {
	msg   string
	cause error
}

func (e *wrapErr) Error() string { return e.msg }
func (e *wrapErr) Unwrap() error { return e.cause }

func TestNativeAdaptersRefuseWithoutASecretTerminally(t *testing.T) {
	cases := []struct {
		name string
		a    ChannelAdapter
		ch   *model.NotificationChannel
	}{
		{"slack", &slackAdapter{}, channelWith("c", model.ChannelTypeSlack, "")},
		{"teams", &teamsAdapter{}, channelWith("c", model.ChannelTypeTeams, "")},
		{"pagerduty", &pagerDutyAdapter{}, channelWith("c", model.ChannelTypePagerDuty, "")},
	}
	for _, c := range cases {
		err := c.a.Deliver(context.Background(), c.ch, "", nil, rendered("RAISED"))
		if !errors.Is(err, httpsink.ErrAuthRefused) {
			t.Errorf("%s with no secret: %v; want an ErrAuthRefused so the dispatcher does not retry", c.name, err)
		}
	}
}

func TestNativeAdaptersRefuseARenderingWithoutAnAlarm(t *testing.T) {
	f := newProviderFake(t, 200, "ok")
	empty := &RenderedNotification{Subject: "x"}
	for name, a := range map[string]struct {
		ad ChannelAdapter
		ch *model.NotificationChannel
		s  string
	}{
		"slack":     {&slackAdapter{client: f.srv.Client()}, channelWith("c", model.ChannelTypeSlack, ""), f.srv.URL},
		"teams":     {&teamsAdapter{client: f.srv.Client()}, channelWith("c", model.ChannelTypeTeams, ""), f.srv.URL},
		"pagerduty": {&pagerDutyAdapter{client: f.srv.Client(), endpoint: f.srv.URL}, channelWith("c", model.ChannelTypePagerDuty, ""), "key"},
	} {
		if err := a.ad.Deliver(context.Background(), a.ch, a.s, nil, empty); !errors.Is(err, errNoAlarm) {
			t.Errorf("%s: %v, want errNoAlarm", name, err)
		}
	}
	if f.calls != 0 {
		t.Fatalf("nothing may reach the wire for a rendering with no alarm; %d calls", f.calls)
	}
}

func TestNativeAdaptersRejectBadConfig(t *testing.T) {
	for _, ch := range []*model.NotificationChannel{
		channelWith("c", model.ChannelTypeSlack, `{"nope":1}`),
		channelWith("c", model.ChannelTypeTeams, `{"linkBaseUrl":"ftp://x"}`),
		channelWith("c", model.ChannelTypePagerDuty, `{"region":"mars"}`),
	} {
		var a ChannelAdapter
		switch ch.ChannelType {
		case model.ChannelTypeSlack:
			a = &slackAdapter{}
		case model.ChannelTypeTeams:
			a = &teamsAdapter{}
		default:
			a = &pagerDutyAdapter{}
		}
		if err := a.Deliver(context.Background(), ch, "https://x/y", nil, rendered("RAISED")); err == nil {
			t.Errorf("%s accepted a bad config", ch.ChannelType)
		}
	}
}

func TestTeamsPostsAdaptiveCardInWorkflowsEnvelope(t *testing.T) {
	f := newProviderFake(t, 202, "")
	a := &teamsAdapter{client: f.srv.Client()}
	ch := channelWith("teams-1", model.ChannelTypeTeams, `{"linkBaseUrl":"https://console.example.com"}`)

	if err := a.Deliver(context.Background(), ch, f.srv.URL+"/workflows/abc", nil, rendered("RAISED")); err != nil {
		t.Fatalf("deliver (a Workflows webhook answers 202): %v", err)
	}
	if f.body["type"] != "message" {
		t.Fatalf("envelope type = %v", f.body["type"])
	}
	att := asMaps(t, f.body["attachments"])
	if att[0]["contentType"] != "application/vnd.microsoft.card.adaptive" {
		t.Fatalf("contentType = %v", att[0]["contentType"])
	}
	card := att[0]["content"].(map[string]any)
	if card["type"] != "AdaptiveCard" || card["version"] != "1.4" {
		t.Fatalf("card = %v/%v", card["type"], card["version"])
	}
	body := asMaps(t, card["body"])
	if body[0]["color"] != "Attention" {
		t.Fatalf("CRITICAL title colour = %v", body[0]["color"])
	}
	if !strings.Contains(body[0]["text"].(string), "overheat") {
		t.Fatalf("title = %v", body[0]["text"])
	}
	if body[1]["type"] != "FactSet" || len(body[1]["facts"].([]any)) < 4 {
		t.Fatalf("fact set = %v", body[1])
	}
	if len(body) != 3 || !strings.Contains(body[2]["text"].(string), "Acknowledge") {
		t.Fatalf("an open alarm must carry the ack hint: %v", body)
	}
	actions := asMaps(t, card["actions"])
	if actions[0]["type"] != "Action.OpenUrl" || actions[0]["url"] != "https://console.example.com/alarms" {
		t.Fatalf("actions = %v", actions)
	}
}

func TestTeamsAccentAndOptionalParts(t *testing.T) {
	for ev, want := range map[string]string{"CLEARED": "Good", "ACKNOWLEDGED": "Accent"} {
		if got := teamsAccent(AlarmFacts{EventType: ev, Severity: "CRITICAL"}); got != want {
			t.Errorf("%s accent = %q, want %q", ev, got, want)
		}
	}
	for sev, want := range map[string]string{"CRITICAL": "Attention", "MAJOR": "Attention", "MINOR": "Warning", "WARNING": "Warning", "INDETERMINATE": "Default"} {
		if got := teamsAccent(AlarmFacts{EventType: "RAISED", Severity: sev}); got != want {
			t.Errorf("%s accent = %q, want %q", sev, got, want)
		}
	}
	card := teamsMessage(rendered("CLEARED"), "")["attachments"].([]map[string]any)[0]["content"].(map[string]any)
	if _, has := card["actions"]; has {
		t.Fatal("no link configured, so no action")
	}
	if n := len(card["body"].([]map[string]any)); n != 2 {
		t.Fatalf("a cleared alarm carries no ack hint; body has %d blocks", n)
	}
}

func TestTeamsNon2xxFailsAndLeaksNoURL(t *testing.T) {
	f := newProviderFake(t, 400, "bad")
	a := &teamsAdapter{client: f.srv.Client()}
	err := a.Deliver(context.Background(), channelWith("t", model.ChannelTypeTeams, ""), f.srv.URL+"/workflows/SECRETSIG", nil, rendered("RAISED"))
	if err == nil {
		t.Fatal("a 400 must fail")
	}
	if strings.Contains(err.Error(), "SECRETSIG") {
		t.Fatalf("leaked: %v", err)
	}
}

func TestPagerDutyLifecycleMapsToTriggerAcknowledgeResolveWithDedupKey(t *testing.T) {
	cases := []struct {
		event, action string
	}{
		{"RAISED", "trigger"},
		{"ESCALATED", "trigger"},
		{"DEESCALATED", "trigger"},
		{"RENOTIFIED", "trigger"},
		{"ACKNOWLEDGED", "acknowledge"},
		{"CLEARED", "resolve"},
	}
	for _, c := range cases {
		f := newProviderFake(t, 202, `{"status":"success"}`)
		a := &pagerDutyAdapter{client: f.srv.Client(), endpoint: f.srv.URL + "/v2/enqueue"}
		ch := channelWith("pd", model.ChannelTypePagerDuty, "")
		if err := a.Deliver(context.Background(), ch, "ROUTINGKEY", nil, rendered(c.event)); err != nil {
			t.Fatalf("%s: %v", c.event, err)
		}
		if f.path != "/v2/enqueue" {
			t.Fatalf("path = %q", f.path)
		}
		if f.body["event_action"] != c.action {
			t.Errorf("%s -> %v, want %s", c.event, f.body["event_action"], c.action)
		}
		if f.body["dedup_key"] != "alarm-7" {
			t.Errorf("%s dedup_key = %v, want the alarm token", c.event, f.body["dedup_key"])
		}
		if f.body["routing_key"] != "ROUTINGKEY" {
			t.Errorf("%s routing_key = %v", c.event, f.body["routing_key"])
		}
		_, hasPayload := f.body["payload"]
		if (c.action == "trigger") != hasPayload {
			t.Errorf("%s: payload present = %v; only a trigger carries one", c.event, hasPayload)
		}
	}
}

func TestPagerDutyTriggerPayload(t *testing.T) {
	f := newProviderFake(t, 202, "{}")
	a := &pagerDutyAdapter{client: f.srv.Client(), endpoint: f.srv.URL}
	ch := channelWith("pd", model.ChannelTypePagerDuty, `{"source":"plant-3","linkBaseUrl":"https://console.example.com"}`)
	if err := a.Deliver(context.Background(), ch, "K", nil, rendered("RAISED")); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	p := f.body["payload"].(map[string]any)
	if p["severity"] != "critical" || p["source"] != "plant-3" || p["summary"] != "[CRITICAL] Alarm: overheat" {
		t.Fatalf("payload = %v", p)
	}
	if p["class"] != "overheat" || p["group"] != "device 42" || p["timestamp"] != "2026-11-06T10:01:00Z" {
		t.Fatalf("payload = %v", p)
	}
	cd := p["custom_details"].(map[string]any)
	if cd["metric"] != "temp" || cd["value"] != 91.5 || cd["alarmToken"] != "alarm-7" {
		t.Fatalf("custom_details = %v", cd)
	}
	links := asMaps(t, f.body["links"])
	if links[0]["href"] != "https://console.example.com/alarms" {
		t.Fatalf("links = %v", links)
	}
}

func TestPagerDutySeverityMapping(t *testing.T) {
	for in, want := range map[string]string{
		"CRITICAL": "critical", "MAJOR": "error", "MINOR": "warning", "WARNING": "warning", "INDETERMINATE": "info", "": "info",
	} {
		if got := pdSeverity(in); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}

func TestPagerDutyUnknownEventTypeFailsLoudly(t *testing.T) {
	f := newProviderFake(t, 202, "{}")
	a := &pagerDutyAdapter{client: f.srv.Client(), endpoint: f.srv.URL}
	err := a.Deliver(context.Background(), channelWith("pd", model.ChannelTypePagerDuty, ""), "K", nil, rendered("MYSTERY"))
	if err == nil || f.calls != 0 {
		t.Fatalf("an unclassified transition must not page anyone: err=%v calls=%d", err, f.calls)
	}
}

func TestPagerDutyRegionPicksHost(t *testing.T) {
	if pdEndpoint("us") != "https://events.pagerduty.com/v2/enqueue" || pdEndpoint("eu") != "https://events.eu.pagerduty.com/v2/enqueue" || pdEndpoint("") != pdEndpoint("us") {
		t.Fatalf("endpoints: %s %s", pdEndpoint("us"), pdEndpoint("eu"))
	}
	cfg, err := model.ParsePagerDutyConfig("c", strPtr(`{"region":"eu"}`))
	if err != nil || cfg.Region != model.PagerDutyRegionEU {
		t.Fatalf("cfg = %+v, %v", cfg, err)
	}
}

func strPtr(s string) *string { return &s }

func TestPagerDutyRejectsNon2xx(t *testing.T) {
	f := newProviderFake(t, 400, `{"status":"invalid event","errors":["Event object is invalid"]}`)
	a := &pagerDutyAdapter{client: f.srv.Client(), endpoint: f.srv.URL}
	err := a.Deliver(context.Background(), channelWith("pd", model.ChannelTypePagerDuty, ""), "K", nil, rendered("RAISED"))
	if err == nil || !strings.Contains(err.Error(), "400") {
		t.Fatalf("err = %v", err)
	}
}

// The guard, not the adapter, is the destination boundary: a native channel built the way
// production builds it (egressClient) refuses a loopback destination, and the refusal
// carries the egress sentinel the dispatcher treats as terminal.
func TestNativeAdaptersDialThroughTheEgressGuard(t *testing.T) {
	f := newProviderFake(t, 200, "ok")
	reg := newAdapterRegistry(egress.NewGuard(nil), nil)
	for _, tc := range []struct {
		typ    string
		secret string
	}{
		{model.ChannelTypeSlack, f.srv.URL + "/h"},
		{model.ChannelTypeTeams, f.srv.URL + "/h"},
	} {
		err := reg[tc.typ].Deliver(context.Background(), channelWith("c", tc.typ, ""), tc.secret, nil, rendered("RAISED"))
		if !errors.Is(err, egress.ErrBlocked) {
			t.Errorf("%s to a loopback address: %v, want egress.ErrBlocked", tc.typ, err)
		}
	}
	if f.calls != 0 {
		t.Fatalf("the guard let %d request(s) through to a loopback server", f.calls)
	}
}

func TestRegistryHasAnAdapterForEveryAvailableChannelType(t *testing.T) {
	reg := newAdapterRegistry(nil, nil)
	for _, ct := range model.SupportedChannelTypes {
		if _, ok := reg[ct.Id]; ct.Available && !ok {
			t.Errorf("channel type %q is marked available but has no adapter", ct.Id)
		}
	}
	for id := range reg {
		if !model.IsKnownChannelType(id) {
			t.Errorf("adapter registered for %q, which is not in the catalog", id)
		}
	}
}

func TestRenderCarriesTypedAlarmFacts(t *testing.T) {
	r := renderEscalation(&model.NotificationState{AlarmToken: "t1", AlarmKey: "k", Severity: "MAJOR", NotifyCount: 2}, 3)
	if r.Alarm.EventType != "RENOTIFIED" || r.Alarm.AlarmToken != "t1" || r.Alarm.Escalation != 3 || r.Alarm.Severity != "MAJOR" {
		t.Fatalf("escalation facts = %+v", r.Alarm)
	}
	if !strings.Contains(r.Alarm.title(r), "escalation 3") {
		t.Fatalf("title = %q", r.Alarm.title(r))
	}
	if got := (AlarmFacts{}).title(&RenderedNotification{Subject: "subj"}); got != "subj" {
		t.Fatalf("a keyless alarm falls back to the subject, got %q", got)
	}
}

func TestTruncAndLink(t *testing.T) {
	if trunc("abc", 3) != "abc" || trunc("abcd", 3) != "ab…" {
		t.Fatalf("trunc: %q %q", trunc("abc", 3), trunc("abcd", 3))
	}
	if alarmLink("") != "" || alarmLink("https://c") != "https://c/alarms" {
		t.Fatal("alarmLink")
	}
}

func TestRenderNotificationCarriesTypedAlarmFacts(t *testing.T) {
	v := 3.5
	ev := &dmmodel.AlarmStateChangeEvent{
		EventType: dmmodel.AlarmEventCleared, AlarmToken: "tk", AlarmKey: "key", OriginatorType: "device",
		OriginatorId: 9, Severity: "MINOR", State: "CLEARED", LastValue: &v, MetricKey: "m",
	}
	a := renderNotification(ev).Alarm
	if a.EventType != "CLEARED" || a.AlarmToken != "tk" || a.Originator != "device 9" || a.Severity != "MINOR" ||
		a.Value == nil || *a.Value != 3.5 || a.MetricKey != "m" {
		t.Fatalf("facts = %+v", a)
	}
}

func TestTeamsTransportErrorNeverContainsTheWebhookURL(t *testing.T) {
	f := newProviderFake(t, 202, "")
	secretURL := f.srv.URL + "/workflows/SECRETSIG"
	f.srv.Close()
	a := &teamsAdapter{client: f.srv.Client()}
	err := a.Deliver(context.Background(), channelWith("t", model.ChannelTypeTeams, ""), secretURL, nil, rendered("RAISED"))
	if err == nil || strings.Contains(err.Error(), "SECRETSIG") {
		t.Fatalf("transport error leaked the secret URL (or succeeded): %v", err)
	}
}
