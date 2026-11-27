// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package testkit

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"
)

// Canary is a distinctive fake secret. Cores are the parts of Value that
// cannot occur by accident; the harness looks for them, and for every
// 12-byte window of them, so a leak of part of a secret is caught too.
type Canary struct {
	Label string
	Value string
	Cores []string
}

// Window is the length of the substrings of a canary's cores the harness
// searches for.
const Window = 12

// Canaries returns one fake value per class of secret the installer handles.
// The kubeconfig token is all lower case on purpose: it looks like an
// identifier, so it exercises the refusal of secrets in identifier fields.
func Canaries() []Canary {
	return []Canary{
		{Label: "root-key", Value: "dcCANARYrootKEY7Qm2xV9pL4wZ8nB3tR6yH1cJ5kF0sA=",
			Cores: []string{"dcCANARYrootKEY7Qm2xV9pL4wZ8nB3tR6yH1cJ5kF0sA"}},
		{Label: "escrow-passphrase", Value: `dc canary "pass&phrase" Zx9?Qv=7/Wb2+Lm`,
			Cores: []string{`canary "pass&phrase" Zx9?Qv=7/Wb2+Lm`}},
		{Label: "superuser-seed", Value: "dcCanarySeedPw-8Kp3Wm7Rt2Vn",
			Cores: []string{"dcCanarySeedPw-8Kp3Wm7Rt2Vn"}},
		{Label: "smtp-password", Value: "dcCanarySmtpPw_Lq4Hd9Xe6Gu",
			Cores: []string{"dcCanarySmtpPw_Lq4Hd9Xe6Gu"}},
		{Label: "webhook-url", Value: "https://hooks.example.invalid/services/dcCanaryHOOK/T9xK2mP7vQ4",
			Cores: []string{"dcCanaryHOOK/T9xK2mP7vQ4"}},
		{Label: "s3-secret-key", Value: "dcCanaryS3SecretKey/Rb8Nf3Jw6Yc1Tg5",
			Cores: []string{"dcCanaryS3SecretKey/Rb8Nf3Jw6Yc1Tg5"}},
		{Label: "kubeconfig", Value: "apiVersion: v1\nkind: Config\nusers:\n- name: admin\n  user:\n" +
			"    token: dccanarykubetoken9w8x7v6u5t4s3r\n" +
			"    client-key-data: dcCanaryKubeKEYdata4Hq8Zt1Mv6Xc\n",
			Cores: []string{"dccanarykubetoken9w8x7v6u5t4s3r", "dcCanaryKubeKEYdata4Hq8Zt1Mv6Xc"}},
		// A value holding an invisible format character (a ZWJ) and a quote. It is
		// drawn without the ZWJ, and quoted in JSON it is drawn with the escape and
		// without the ZWJ: a form neither the raw value nor its plain sanitised form
		// matches.
		{Label: "format-char", Value: "dcCanaryFmt\"Join\u200der5Rk8Vd3Hx2Qm",
			Cores: []string{"dcCanaryFmt\"Join", "er5Rk8Vd3Hx2Qm"}},
	}
}

// RegisterCanaries registers every canary with a hub's redactor.
func RegisterCanaries(r interface{ AddSecret(string) error }) error {
	for _, c := range Canaries() {
		if err := r.AddSecret(c.Value); err != nil {
			return fmt.Errorf("canary %s: %w", c.Label, err)
		}
	}
	return nil
}

// needles returns what a leak of c could look like: the value and its cores
// in the encodings tool output uses, and every window of each core.
func (c Canary) needles() []string {
	var out []string
	add := func(s string) {
		out = append(out, s, url.QueryEscape(s), url.PathEscape(s),
			base64.StdEncoding.EncodeToString([]byte(s)), base64.RawURLEncoding.EncodeToString([]byte(s)))
		if j, err := json.Marshal(s); err == nil {
			out = append(out, strings.Trim(string(j), `"`))
		}
	}
	add(c.Value)
	for _, core := range c.Cores {
		add(core)
		for i := 0; i+Window <= len(core); i++ {
			out = append(out, core[i:i+Window])
		}
	}
	return out
}

// FindCanaries returns a description of every canary found in data.
func FindCanaries(data []byte) []string {
	var found []string
	for _, c := range Canaries() {
		for _, n := range c.needles() {
			if len(n) >= Window && bytes.Contains(data, []byte(n)) {
				found = append(found, fmt.Sprintf("%s (as %q)", c.Label, n))
				break
			}
		}
	}
	return found
}

// AssertNoCanary fails t if any canary appears in data.
func AssertNoCanary(t testing.TB, where string, data []byte) {
	t.Helper()
	if leaks := FindCanaries(data); len(leaks) > 0 {
		t.Errorf("%s leaks %s", where, strings.Join(leaks, ", "))
	}
}

// Dump renders values every way a presenter, a log or a debugger might: JSON,
// and the %v, %+v and %#v verbs. A marshalling error is rendered too, since
// error text is an output path of its own.
func Dump(values ...any) []byte {
	var b bytes.Buffer
	for _, v := range values {
		if j, err := json.Marshal(v); err != nil {
			fmt.Fprintf(&b, "marshal error: %v\n", err)
		} else {
			b.Write(j)
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "%v\n%+v\n%#v\n", v, v, v)
	}
	return b.Bytes()
}
