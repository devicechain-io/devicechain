// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package product

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Finding is one place a bundle holds something shaped like a secret value. It names the
// place and the rule, never the value: a scanner that printed what it found would itself be
// the leak.
type Finding struct {
	Where string
	Rule  string
}

func (f Finding) String() string { return f.Where + ": " + f.Rule }

// sensitiveKeys are document keys whose value is a secret by name. Keys are compared
// lowercased with underscores and hyphens removed.
var sensitiveKeys = map[string]bool{
	"password": true, "passwd": true, "secret": true, "secretkey": true, "secretaccesskey": true,
	"clientsecret": true, "apikey": true, "privatekey": true, "accesstoken": true,
	"authtoken": true, "bearertoken": true, "authorization": true, "sessiontoken": true,
}

// valueRules are shapes that are a secret wherever they appear.
var valueRules = []struct {
	name string
	re   *regexp.Regexp
}{
	{"a PEM private key", regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----`)},
	{"an AWS access key id", regexp.MustCompile(`\b(AKIA|ASIA)[0-9A-Z]{16}\b`)},
	{"a JSON web token", regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]*`)},
	{"a bearer credential", regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/=-]{20,}`)},
	{"credentials embedded in a URL", regexp.MustCompile(`://[^/\s:@]+:[^/\s@]+@`)},
}

// Scan reports everything in the bundle that looks like a secret VALUE. A credential handle
// ("env:NAME") is the sanctioned form and is never reported.
func (b *Bundle) Scan() []Finding {
	var out []Finding
	walk := func(where string, v any) {
		out = append(out, scanValue(where, "", v)...)
	}
	each := func(where string, v any) {
		raw, err := json.Marshal(v)
		if err != nil {
			return
		}
		var g any
		if json.Unmarshal(raw, &g) == nil {
			walk(where, g)
		}
	}
	for _, t := range b.DeviceTypes {
		each(dirDeviceTypes+"/"+t.Token, t)
	}
	for _, p := range b.Profiles {
		each(dirProfiles+"/"+p.Token, p)
	}
	for _, in := range b.Integrations {
		each(dirIntegrations+"/"+in.Token, in)
	}
	for _, c := range b.Connectors {
		each(dirConnectors+"/"+c.Token, c)
	}
	for _, d := range b.Dashboards {
		each(dirDashboards+"/"+d.Token, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}

func scanValue(where, key string, v any) []Finding {
	var out []Finding
	switch x := v.(type) {
	case map[string]any:
		for k, c := range x {
			out = append(out, scanValue(where+"."+k, k, c)...)
		}
	case []any:
		for i, c := range x {
			out = append(out, scanValue(fmt.Sprintf("%s[%d]", where, i), key, c)...)
		}
	case string:
		if sensitiveKeys[normKey(key)] && strings.TrimSpace(x) != "" && !isHandle(x) {
			out = append(out, Finding{where, "a value under the secret-named key " + key})
		}
		for _, r := range valueRules {
			if r.re.MatchString(x) {
				out = append(out, Finding{where, "looks like " + r.name})
			}
		}
	case float64:
		if sensitiveKeys[normKey(key)] {
			out = append(out, Finding{where, "a value under the secret-named key " + key})
		}
	}
	return out
}

func isHandle(s string) bool {
	_, err := parseHandle(s)
	return err == nil
}

func normKey(k string) string {
	k = strings.ToLower(k)
	k = strings.ReplaceAll(k, "_", "")
	return strings.ReplaceAll(k, "-", "")
}

// scanBundle is the check an export applies: any finding refuses the write.
func scanBundle(b *Bundle) error {
	return ScanError(b.Scan())
}

// ScanError turns findings into the error a command reports, or nil for none.
func ScanError(fs []Finding) error {
	if len(fs) == 0 {
		return nil
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "the bundle holds %d value(s) shaped like a secret; a bundle carries secrets as handles (credential: {handle: env:NAME}), never as values:", len(fs))
	for _, f := range fs {
		sb.WriteString("\n  " + f.String())
	}
	return fmt.Errorf("%s", sb.String())
}
