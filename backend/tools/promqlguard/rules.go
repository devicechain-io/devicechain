// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package promqlguard

import (
	"fmt"
	"os"
	"regexp"

	"gopkg.in/yaml.v3"
)

// Alert is one alerting rule, located well enough that a finding names something a
// reader can open.
type Alert struct {
	File  string
	Group string
	Name  string
	Expr  string
}

// Location renders an Alert as file:group:name, which is what a finding leads with.
func (a Alert) Location() string {
	return fmt.Sprintf("%s: group %q, alert %q", a.File, a.Group, a.Name)
}

type ruleFile struct {
	Groups []struct {
		Name  string `yaml:"name"`
		Rules []struct {
			Alert  string `yaml:"alert"`
			Record string `yaml:"record"`
			Expr   string `yaml:"expr"`
		} `yaml:"rules"`
	} `yaml:"groups"`
}

// 🔴 THE SECOND, INDEPENDENT COUNT. This matches the `alert:` key in the raw bytes,
// which the YAML tree walk below never looks at. The point is not that a regex is a
// good way to read YAML — it is not, and it is not used as one — but that a scraper
// and a parser failing the SAME way is far less likely than either failing alone.
//
// The previous, removed version of this check scraped expressions with a regex that
// looked ahead to `for|labels|annotations|record` for the end of the expression, so an
// alert whose `expr` was the last key, or was followed by `keep_firing_for:` (a real
// Prometheus field), was silently skipped. It then REPORTED A COUNT — "1 alert
// expression(s)" over a two-alert file — which is worse than no gate at all, because a
// number that looks like coverage is read as coverage. This is what stops the same
// class of narrowing happening to the YAML path.
var alertKey = regexp.MustCompile(`(?m)^[ \t]*-?[ \t]*alert:[ \t]`)

// LoadFile reads one promtool-format rule file and returns every alerting rule in it.
//
// Recording rules are excluded on purpose — they compute a series and are not expected
// to be able to return nothing — and they are excluded by the ABSENCE of an `alert:`
// key rather than by the presence of a `record:` one, so both counts agree about which
// rules are in scope.
//
// It fails when the two counts disagree. A file this reads only part of has no verdict
// to give, and a partial verdict reported as a clean one is the exact shape the guard
// exists to prevent one level down.
func LoadFile(path string) ([]Alert, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("could not read %s, so it was not checked: %w", path, err)
	}

	var rf ruleFile
	if err := yaml.Unmarshal(raw, &rf); err != nil {
		return nil, fmt.Errorf("could not parse %s as a rule file, so it was not checked: %w", path, err)
	}

	var alerts []Alert
	for _, g := range rf.Groups {
		for _, r := range g.Rules {
			if r.Alert == "" {
				continue
			}
			alerts = append(alerts, Alert{File: path, Group: g.Name, Name: r.Alert, Expr: r.Expr})
		}
	}

	if textual := len(alertKey.FindAllIndex(raw, -1)); textual != len(alerts) {
		return nil, fmt.Errorf(
			"%s: the YAML walk found %d alert(s) but the file's own text carries %d `alert:` key(s).\n"+
				"  The two disagree, so one of them is reading only part of this file — and a\n"+
				"  partial reading reported as a clean result is precisely what this check exists\n"+
				"  to stop happening to the rules it reads",
			path, len(alerts), textual)
	}

	return alerts, nil
}

// Check runs every alert in the given rule files past CheckExpr.
//
// It returns the findings and the number of alerts it actually examined. The caller
// holds that count against a floor: zero findings is what a clean corpus reports AND
// what a run over nothing reports, and those must not exit the same way.
func Check(paths []string) ([]Finding, int, error) {
	var (
		findings []Finding
		checked  int
	)
	for _, path := range paths {
		alerts, err := LoadFile(path)
		if err != nil {
			return nil, 0, err
		}
		for _, a := range alerts {
			checked++
			why, err := CheckExpr(a.Expr)
			if err != nil {
				return nil, 0, fmt.Errorf("%s: %w", a.Location(), err)
			}
			if why != "" {
				findings = append(findings, Finding{Alert: a, Reason: why})
			}
		}
	}
	return findings, checked, nil
}

// Finding is one alert that can never return nothing.
type Finding struct {
	Alert  Alert
	Reason string
}

func (f Finding) String() string {
	return fmt.Sprintf("%s\n    expr:   %s\n    always fires because %s", f.Alert.Location(), f.Alert.Expr, f.Reason)
}
