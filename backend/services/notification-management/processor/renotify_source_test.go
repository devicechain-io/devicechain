// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-notification-management/model"
)

// pagedThenDue pages an alarm through Notify and returns a notifier whose clock is past the
// escalation window, so an Escalate call re-notifies it.
func pagedThenDue(t *testing.T, ev *dmmodel.AlarmStateChangeEvent) (*PolicyNotifier, *msgRecorder, *model.Api, context.Context) {
	t.Helper()
	api := fencedApi(t)
	ctx := tenantScoped()
	seedEscalatingPolicy(t, api, ctx)
	rec := &msgRecorder{}
	n := testNotifier(map[string]ChannelAdapter{model.ChannelTypeSMTP: rec})
	n.api = api
	n.store = &fakeSecretStore{}
	if err := n.Notify(ctx, ev); err != nil {
		t.Fatal(err)
	}
	n.now = func() time.Time { return time.Now().Add(time.Hour) }
	return n, rec, api, ctx
}

func namedAlarm() *dmmodel.AlarmStateChangeEvent {
	raised := time.Now().UTC().Add(-time.Minute)
	ev := cycleEvent(dmmodel.AlarmEventRaised, raised, raised)
	ev.OriginatorType, ev.OriginatorId = "device", 42
	ev.DeviceToken, ev.DeviceName, ev.DeviceTypeToken = "boiler-7", "Boiler 7", "boiler"
	return ev
}

// A timed re-notification is rendered from the state row, so the row has to remember the device.
func TestARenotificationNamesTheDevice(t *testing.T) {
	n, rec, api, ctx := pagedThenDue(t, namedAlarm())
	states, err := api.OpenNotificationStates(ctx)
	if err != nil || len(states) != 1 {
		t.Fatalf("open states = %v, %v", states, err)
	}
	pol, _ := api.EnabledNotificationPolicies(ctx)
	if err := n.Escalate(ctx, states[0], pol, n.clock(), 5); err != nil {
		t.Fatal(err)
	}
	if got := rec.types(); len(got) != 2 || got[1] != "RENOTIFIED" {
		t.Fatalf("sent %v, want [RAISED RENOTIFIED]", got)
	}
	m := rec.sent[1]
	if !strings.Contains(m.Subject, "on Boiler 7") || strings.Contains(m.Subject, "device 42") {
		t.Fatalf("summary should name the device: %q", m.Subject)
	}
	for k, want := range map[string]any{
		"deviceToken": "boiler-7", "deviceName": "Boiler 7", "deviceTypeToken": "boiler",
		"originatorType": "device", "originatorId": uint(42),
	} {
		if m.Payload[k] != want {
			t.Errorf("payload[%s] = %v, want %v", k, m.Payload[k], want)
		}
	}
	if m.Alarm.DeviceToken != "boiler-7" || m.Alarm.DeviceName != "Boiler 7" || m.Alarm.Originator != "Boiler 7" {
		t.Errorf("facts = %+v", m.Alarm)
	}
	if !strings.Contains(m.TextBody, "Boiler 7") {
		t.Errorf("body omits the device: %q", m.TextBody)
	}
}

// A row that knows no device (written before the columns) renders without one, and invents nothing.
func TestARenotificationOfARowWithoutASourceOmitsIt(t *testing.T) {
	at := time.Now().UTC().Add(-time.Minute)
	n, rec, api, ctx := pagedThenDue(t, cycleEvent(dmmodel.AlarmEventRaised, at, at))
	states, _ := api.OpenNotificationStates(ctx)
	pol, _ := api.EnabledNotificationPolicies(ctx)
	if err := n.Escalate(ctx, states[0], pol, n.clock(), 5); err != nil {
		t.Fatal(err)
	}
	m := rec.sent[1]
	for _, k := range []string{"deviceToken", "deviceName", "deviceTypeToken", "originatorType", "originatorId"} {
		if _, ok := m.Payload[k]; ok {
			t.Errorf("payload carries unknown %s: %v", k, m.Payload[k])
		}
	}
	if strings.Contains(m.Subject, " on ") || m.Alarm.Originator != "" {
		t.Errorf("an unknown source was listed: %q / %q", m.Subject, m.Alarm.Originator)
	}
}

// A later event that does not state a field must not erase what the row remembers.
func TestALaterEventWithoutNamesKeepsTheStoredSource(t *testing.T) {
	ev := namedAlarm()
	n, _, api, ctx := pagedThenDue(t, ev)
	again := *ev
	again.EventType = dmmodel.AlarmEventEscalated
	again.OccurredTime = ev.OccurredTime.Add(time.Second)
	again.DeviceName, again.DeviceToken, again.DeviceTypeToken = "", "", ""
	again.OriginatorType, again.OriginatorId = "", 0
	if err := n.Notify(ctx, &again); err != nil {
		t.Fatal(err)
	}
	states, _ := api.OpenNotificationStates(ctx)
	if got := states[0].Source(); got.DeviceName != "Boiler 7" || got.DeviceToken != "boiler-7" || got.OriginatorId != 42 {
		t.Fatalf("stored source = %+v", got)
	}
}

// The scheduler's late-clear pickup rebuilds the clear from the row; it names the device too.
func TestAPickedUpClearNamesTheDevice(t *testing.T) {
	api := fencedApi(t)
	ctx := tenantScoped()
	seedClearPolicy(t, api, ctx, true)
	rec := &msgRecorder{}
	n := testNotifier(map[string]ChannelAdapter{model.ChannelTypeSMTP: rec})
	n.api = api
	n.store = &fakeSecretStore{}
	ev := namedAlarm()
	// Paged, then stamped cleared after the policy was saved, with no clear message to announce it.
	if err := n.Notify(ctx, ev); err != nil {
		t.Fatal(err)
	}
	if err := api.MarkCleared(ctx, ev.AlarmToken, "k", "CRITICAL", ev.RaisedTime, time.Now().UTC().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	policies, _ := api.EnabledNotificationPolicies(ctx)
	if err := n.AnnouncePendingClears(ctx, policies, time.Now().Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got := rec.types(); len(got) != 2 || got[1] != "CLEARED" {
		t.Fatalf("sent %v, want [RAISED CLEARED]", got)
	}
	m := rec.sent[1]
	if m.Payload["deviceName"] != "Boiler 7" || m.Payload["deviceToken"] != "boiler-7" ||
		m.Payload["originatorType"] != "device" || !strings.Contains(m.Subject, "on Boiler 7") {
		t.Fatalf("the picked-up clear does not name the device: %q %v", m.Subject, m.Payload)
	}
}

func deliverWebhookTo(t *testing.T, config func(url string) string, msg *RenderedNotification) map[string]any {
	t.Helper()
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
	}))
	defer srv.Close()
	a := &webhookAdapter{client: srv.Client()}
	ch := channelWith("hook", model.ChannelTypeWebhook, config(srv.URL))
	if err := a.Deliver(context.Background(), ch, "", nil, msg); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	return got
}

func TestWebhookLinksWhenItHasAConsoleURL(t *testing.T) {
	msg := renderNotification(namedEvent())
	got := deliverWebhookTo(t, func(u string) string {
		return `{"url":"` + u + `","auth":"none","linkBaseUrl":"https://console.example/"}`
	}, msg)
	if got["alarmUrl"] != "https://console.example/alarms" || got["deviceUrl"] != "https://console.example/devices/boiler-7" {
		t.Fatalf("links = %v / %v", got["alarmUrl"], got["deviceUrl"])
	}
	// Existing keys are untouched.
	if got["text"] != msg.Subject || got["deviceName"] != "Boiler 7" || got["alarmToken"] != "alarm-1" {
		t.Fatalf("existing keys changed: %v", got)
	}
}

func TestWebhookWithoutAConsoleURLAddsNoLinkKeys(t *testing.T) {
	got := deliverWebhookTo(t, func(u string) string { return `{"url":"` + u + `","auth":"none"}` },
		renderNotification(namedEvent()))
	for _, k := range []string{"alarmUrl", "deviceUrl"} {
		if _, ok := got[k]; ok {
			t.Fatalf("payload carries %s with no linkBaseUrl", k)
		}
	}
}

// An alarm that does not name its device gets the alarm link only, never a made-up device link.
func TestWebhookDoesNotLinkAnUnnamedDevice(t *testing.T) {
	e := namedEvent()
	e.DeviceToken = ""
	got := deliverWebhookTo(t, func(u string) string {
		return `{"url":"` + u + `","auth":"none","linkBaseUrl":"https://console.example"}`
	}, renderNotification(e))
	if got["alarmUrl"] != "https://console.example/alarms" {
		t.Fatalf("alarmUrl = %v", got["alarmUrl"])
	}
	if _, ok := got["deviceUrl"]; ok {
		t.Fatalf("deviceUrl = %v", got["deviceUrl"])
	}
}

func TestANewerEventUpdatesSourceAfterEscalation(t *testing.T) {
	event := namedAlarm()
	n, _, api, ctx := pagedThenDue(t, event)
	states, err := api.OpenNotificationStates(ctx)
	if err != nil || len(states) != 1 {
		t.Fatalf("states = %v, %v", states, err)
	}
	escAt := n.clock()
	if err := n.Escalate(ctx, states[0], enabledPolicies(t, api, ctx), escAt, 5); err != nil {
		t.Fatal(err)
	}
	newer := *event
	newer.EventType = dmmodel.AlarmEventEscalated
	newer.OccurredTime = event.OccurredTime.Add(time.Minute)
	newer.DeviceName = "New"
	if err := n.Notify(ctx, &newer); err != nil {
		t.Fatal(err)
	}
	states, err = api.OpenNotificationStates(ctx)
	if err != nil || len(states) != 1 {
		t.Fatalf("states = %v, %v", states, err)
	}
	row := states[0]
	if row.EscalationLevel != 1 || row.Source().DeviceName != "New" || !row.SourceAt.Valid || !row.SourceAt.Time.Equal(newer.OccurredTime) || !row.LastNotifiedAt.Time.Equal(escAt) {
		t.Fatalf("post-escalation source state = %+v", row)
	}
}
