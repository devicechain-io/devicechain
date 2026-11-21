// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package premises validates spike evidence without coupling public-root pins to fleet gates.
package premises

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

type Verdict string

const (
	Holds        Verdict = "HOLDS"
	Fails        Verdict = "FAILS"
	Inconclusive Verdict = "INCONCLUSIVE"
)

type Status string

const (
	OK    Status = "OK"
	Stale Status = "STALE"
)

var (
	ErrInvalidRecord  = errors.New("invalid premise record")
	ErrPremiseNotHeld = errors.New("premise does not hold")
	// ErrNoFleetRoots means there is no fleet root to take pins from yet. It is reported, never
	// papered over with a default: a gate that guesses a pin would pass for the wrong reason.
	ErrNoFleetRoots = errors.New("no fleet roots")
	validID         = regexp.MustCompile(`^E-([a-l]|f2|g-(pg|gcs)|j-[1-5])$`)
	validDigest     = regexp.MustCompile(`^[0-9a-f]{64}$`)
	exactVersion    = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+([+-][0-9A-Za-z.-]+)?$`)
)

// Record contains reviewed evidence. Absence is deliberately different from HOLDS.
type Record struct {
	SchemaVersion int       `json:"schema_version"`
	Experiments   []Premise `json:"experiments"`
}

type Premise struct {
	ID                string         `json:"id"`
	Verdict           Verdict        `json:"verdict"`
	Measured          map[string]any `json:"measured"`
	TofuVersion       string         `json:"tofu_version"`
	ProviderVersion   string         `json:"provider_version"`
	KubernetesVersion string         `json:"kubernetes_version,omitempty"`
	Date              string         `json:"date"`
	TranscriptSHA256  string         `json:"transcript_sha256"`
	Binds             []string       `json:"binds"`
}

// Pins are supplied by the caller from the fleet roots' exact pins, never deploy/gke.
// Kubernetes is the version measured by Kubernetes-bound experiments.
type Pins struct {
	Provider   string
	Tofu       string
	Kubernetes string
}

func Parse(reader io.Reader) (Record, error) {
	var record Record
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return Record{}, fmt.Errorf("%w: %v", ErrInvalidRecord, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return Record{}, fmt.Errorf("%w: trailing data", ErrInvalidRecord)
	}
	if err := record.Validate(); err != nil {
		return Record{}, err
	}
	return record, nil
}

func (r Record) Validate() error {
	if r.SchemaVersion != 1 || r.Experiments == nil {
		return fmt.Errorf("%w: schema_version must be 1 and experiments must be an array", ErrInvalidRecord)
	}
	seen := make(map[string]bool)
	for _, p := range r.Experiments {
		if !validID.MatchString(p.ID) || seen[p.ID] {
			return fmt.Errorf("%w: invalid or duplicate id %q", ErrInvalidRecord, p.ID)
		}
		seen[p.ID] = true
		if p.Verdict != Holds && p.Verdict != Fails && p.Verdict != Inconclusive {
			return fmt.Errorf("%w: %s verdict", ErrInvalidRecord, p.ID)
		}
		if p.Measured != nil && p.Measured["blocked"] != nil && p.Verdict != Inconclusive {
			return fmt.Errorf("%w: %s is blocked, so it cannot be %s", ErrInvalidRecord, p.ID, p.Verdict)
		}
		if p.Measured == nil || !exactVersion.MatchString(p.TofuVersion) || !exactVersion.MatchString(p.ProviderVersion) || !validDigest.MatchString(p.TranscriptSHA256) {
			return fmt.Errorf("%w: %s missing measurements, versions or digest", ErrInvalidRecord, p.ID)
		}
		if _, err := time.Parse(time.DateOnly, p.Date); err != nil {
			return fmt.Errorf("%w: %s date: %v", ErrInvalidRecord, p.ID, err)
		}
		if len(p.Binds) == 0 {
			return fmt.Errorf("%w: %s missing binds", ErrInvalidRecord, p.ID)
		}
		bindings := make(map[string]bool)
		for _, binding := range p.Binds {
			switch binding {
			case "provider", "tofu", "cloud":
			case "kubernetes":
				if !exactVersion.MatchString(p.KubernetesVersion) {
					return fmt.Errorf("%w: %s missing kubernetes version", ErrInvalidRecord, p.ID)
				}
			default:
				return fmt.Errorf("%w: %s unknown binding %q", ErrInvalidRecord, p.ID, binding)
			}
			if bindings[binding] {
				return fmt.Errorf("%w: %s duplicate binding %q", ErrInvalidRecord, p.ID, binding)
			}
			bindings[binding] = true
		}
	}
	return nil
}

// Staleness is pure: callers supply evidence, fleet pins and a clock.
// A cloud-only premise expires after 180 calendar days; mixed bindings follow versions.
func Staleness(record Record, pins Pins, now time.Time) map[string]Status {
	result := make(map[string]Status, len(record.Experiments))
	for _, premise := range record.Experiments {
		status := OK
		if len(premise.Binds) == 0 {
			status = Stale
		}
		for _, component := range premise.Binds {
			var recorded, pinned string
			switch component {
			case "provider":
				recorded, pinned = premise.ProviderVersion, pins.Provider
			case "tofu":
				recorded, pinned = premise.TofuVersion, pins.Tofu
			case "kubernetes":
				recorded, pinned = premise.KubernetesVersion, pins.Kubernetes
			case "cloud":
				continue
			default:
				status = Stale
				continue
			}
			if !exactVersion.MatchString(recorded) || !exactVersion.MatchString(pinned) || recorded != pinned {
				status = Stale
			}
		}
		if len(premise.Binds) == 1 && premise.Binds[0] == "cloud" {
			measured, err := time.Parse(time.DateOnly, premise.Date)
			today := now.UTC().Truncate(24 * time.Hour)
			if err != nil || measured.After(today) || today.After(measured.AddDate(0, 0, 180)) {
				status = Stale
			}
		}
		result[premise.ID] = status
	}
	return result
}

// RequireHolds is separate from staleness so fleet gates can name their required evidence.
func (r Record) RequireHolds(ids ...string) error {
	return r.requireHolds(ids...)
}

func (r Record) requireHolds(ids ...string) error {
	for _, id := range ids {
		found := false
		for _, premise := range r.Experiments {
			if premise.ID == id {
				found = premise.Verdict == Holds
				break
			}
		}
		if !found {
			return fmt.Errorf("%w: %s is absent or non-HOLDS", ErrPremiseNotHeld, id)
		}
	}
	return nil
}

var providerPin = regexp.MustCompile(`(?s)google\s*=\s*\{[^}]*?version\s*=\s*"([^"]*)"`)

// ProviderPinFromRoots reads the google provider's pin from every fleet root under dir
// (dir/gke-*/versions.tf). The roots must agree and the pin must be exact: the premise record
// binds to the roots' own pin, not to deploy/gke.
func ProviderPinFromRoots(dir string) (string, error) {
	files, err := filepath.Glob(filepath.Join(dir, "gke-*", "versions.tf"))
	if err != nil {
		return "", err
	}
	if len(files) == 0 {
		return "", ErrNoFleetRoots
	}
	pin := ""
	for _, file := range files {
		body, err := os.ReadFile(file)
		if err != nil {
			return "", err
		}
		m := providerPin.FindSubmatch(body)
		if m == nil || !exactVersion.MatchString(string(m[1])) {
			return "", fmt.Errorf("%s: the google provider is not pinned to an exact version", file)
		}
		if pin != "" && pin != string(m[1]) {
			return "", fmt.Errorf("%s pins google %s but another fleet root pins %s", file, m[1], pin)
		}
		pin = string(m[1])
	}
	return pin, nil
}
