// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"encoding/json"
	"strings"
	"testing"
)

// A device sets its own name at self-registration, so the name is attacker-controlled text that
// every channel must treat as data.
const hostileName = "<!channel> [click](https://evil.example) *bold* `x`\r\nSubject: forged"

func hostileMessage() *RenderedNotification {
	e := namedEvent()
	e.DeviceName = hostileName
	return renderNotification(e)
}

func TestSlackNeverLetsADeviceNameMentionOrLink(t *testing.T) {
	raw, _ := json.Marshal(slackMessage(hostileMessage(), "", ""))
	body := string(raw)
	// json.Marshal escapes < and > as unicode sequences, so look for the decoded forms too.
	var decoded map[string]any
	_ = json.Unmarshal(raw, &decoded)
	text, _ := decoded["text"].(string)
	if strings.Contains(text, "<!channel>") || strings.Contains(text, "<") {
		t.Fatalf("the top-level text carries an unescaped mention or link: %q", text)
	}
	if strings.Contains(body, "<!channel>") {
		t.Fatalf("an unescaped mention reached the payload: %s", body)
	}
}

func TestTeamsEscapesMarkdownInADeviceName(t *testing.T) {
	msg := teamsMessage(hostileMessage(), "", "")
	raw, _ := json.Marshal(msg)
	var decoded map[string]any
	_ = json.Unmarshal(raw, &decoded)
	card := decoded["attachments"].([]any)[0].(map[string]any)["content"].(map[string]any)
	body := card["body"].([]any)
	title := body[0].(map[string]any)["text"].(string)
	if strings.Contains(title, "[click](") {
		t.Fatalf("the title renders device-authored Markdown: %q", title)
	}
	for _, f := range body[1].(map[string]any)["facts"].([]any) {
		v := f.(map[string]any)["value"].(string)
		if strings.Contains(v, "[click](") || strings.ContainsAny(v, "\r\n") {
			t.Fatalf("a fact renders device-authored Markdown: %q", v)
		}
	}
	// Link and mention syntax is made inert with look-alikes; _ and * are left alone, so a
	// title like over_temp is not shown as over\\_temp.
	if got := teamsEscape("over_temp [a](b) <at>x</at>\n"); strings.ContainsAny(got, "[]()<>\n\\") || !strings.Contains(got, "over_temp") {
		t.Fatalf("teamsEscape = %q", got)
	}
}

func TestEmailKeepsADeviceNameOutOfTheHeadersAndTheBodyOnOneLine(t *testing.T) {
	msg := hostileMessage()
	if strings.ContainsAny(msg.Subject, "\r\n") {
		t.Fatalf("subject has a line break: %q", msg.Subject)
	}
	for _, line := range strings.Split(msg.TextBody, "\n") {
		if strings.HasPrefix(line, "Subject:") {
			t.Fatalf("the body forged a header line: %q", line)
		}
	}
	mail := string(buildEmail("a@x.com", []string{"b@x.com"}, msg))
	head := mail[:strings.Index(mail, "\r\n\r\n")]
	n := 0
	for _, line := range strings.Split(head, "\r\n") {
		if strings.HasPrefix(line, "Subject:") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("headers carry more than one Subject:\n%s", head)
	}
}

func TestANonASCIISubjectIsEncodedAsRFC2047(t *testing.T) {
	e := namedEvent()
	e.DeviceName = "Chaudière ☃"
	mail := string(buildEmail("a@x.com", []string{"b@x.com"}, renderNotification(e)))
	head := mail[:strings.Index(mail, "\r\n\r\n")]
	for _, line := range strings.Split(head, "\r\n") {
		if strings.HasPrefix(line, "Subject:") {
			if !strings.Contains(line, "=?utf-8?q?") {
				t.Fatalf("a non-ASCII subject must be RFC 2047 encoded: %q", line)
			}
			for _, c := range []byte(line) {
				if c > 127 {
					t.Fatalf("raw 8-bit bytes in the Subject header: %q", line)
				}
			}
			return
		}
	}
	t.Fatal("no Subject header")
}
