// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-notification-management/model"
)

func TestDeviceLink(t *testing.T) {
	cases := []struct{ base, token, want string }{
		{"https://c.example", "boiler-7", "https://c.example/devices/boiler-7"},
		{"https://c.example", "a b/c", "https://c.example/devices/a%20b%2Fc"},
		{"", "boiler-7", ""},
		{"https://c.example", "", ""},
	}
	for _, c := range cases {
		if got := deviceLink(c.base, c.token); got != c.want {
			t.Errorf("deviceLink(%q, %q) = %q, want %q", c.base, c.token, got, c.want)
		}
	}
}

// nativeURLs collects every URL the three native messages offer.
func nativeURLs(t *testing.T, msg *RenderedNotification, link, dlink string) map[string][]string {
	t.Helper()
	urls := func(v any) []string {
		raw, _ := json.Marshal(v)
		var out []string
		for _, part := range strings.Split(string(raw), `"`) {
			if strings.HasPrefix(part, "https://console.example") {
				out = append(out, part)
			}
		}
		return out
	}
	cfg := &model.PagerDutyConfig{Source: "devicechain", LinkBaseURL: "https://console.example"}
	if link == "" {
		cfg.LinkBaseURL = ""
	}
	return map[string][]string{
		"slack": urls(slackMessage(msg, link, dlink)),
		"teams": urls(teamsMessage(msg, link, dlink)),
		"pd":    urls(pagerDutyEvent("rk", pdTrigger, cfg, msg)),
	}
}

func TestNativeMessagesLinkTheDevice(t *testing.T) {
	msg := renderNotification(namedEvent())
	base := "https://console.example"
	got := nativeURLs(t, msg, alarmLink(base), deviceLink(base, msg.Alarm.DeviceToken))
	for kind, urls := range got {
		if len(urls) != 2 || urls[0] != base+"/alarms" || urls[1] != base+"/devices/boiler-7" {
			t.Errorf("%s links = %v, want the alarm list then the device page", kind, urls)
		}
	}

	// No console URL configured: no links at all, never a made-up one.
	for kind, urls := range nativeURLs(t, msg, "", "") {
		if len(urls) != 0 {
			t.Errorf("%s offered links %v with no console URL", kind, urls)
		}
	}

	// A console URL but an event that did not name the device: the alarm list only.
	e := namedEvent()
	e.DeviceToken = ""
	unnamed := renderNotification(e)
	for kind, urls := range nativeURLs(t, unnamed, alarmLink(base), deviceLink(base, unnamed.Alarm.DeviceToken)) {
		if len(urls) != 1 || urls[0] != base+"/alarms" {
			t.Errorf("%s links = %v, want only the alarm list", kind, urls)
		}
	}
}
