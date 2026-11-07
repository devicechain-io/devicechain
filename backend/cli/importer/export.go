// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package importer implements `dcctl import` -- a spike that reads the JSON entity exports other
// IoT platforms produce (one record per entity: the entity, its relations, its attributes by
// scope and, for a device, its credentials) and loads what maps cleanly onto DeviceChain through
// the tenant GraphQL API as it stands. It adds no server behaviour.
//
// The work is split in two so it can be reviewed before anything is written. Build turns the
// export into a Plan and a Report with no network at all: every entity that is mapped,
// transformed, partly mapped or not supported is listed once, with the reason. Apply replays
// the Plan in dependency order and is idempotent -- an entity that already exists is counted as
// existing, never overwritten.
//
// Scope of this spike: device profiles, devices (and their access tokens, opt-in), asset
// profiles, assets, relations and attributes. Everything else (customers, alarm rules, rule
// chains, dashboards, entity views, update packages) is catalogued in the report and not
// converted.
package importer

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// EntityID is how an export names another entity.
type EntityID struct {
	EntityType string `json:"entityType"`
	ID         string `json:"id"`
}

// Relation is one directed, typed edge as the export lists it.
type Relation struct {
	From           EntityID        `json:"from"`
	To             EntityID        `json:"to"`
	Type           string          `json:"type"`
	TypeGroup      string          `json:"typeGroup"`
	AdditionalInfo json.RawMessage `json:"additionalInfo,omitempty"`
}

// Attribute is one attribute row; exactly one of the value members is normally set.
type Attribute struct {
	Key          string   `json:"key"`
	LastUpdateTs int64    `json:"lastUpdateTs"`
	BooleanValue *bool    `json:"booleanValue,omitempty"`
	StrValue     *string  `json:"strValue,omitempty"`
	LongValue    *int64   `json:"longValue,omitempty"`
	DoubleValue  *float64 `json:"doubleValue,omitempty"`
	JSONValue    *string  `json:"jsonValue,omitempty"`
}

// Credentials is a device's credentials as the export carries them.
type Credentials struct {
	Type  string  `json:"credentialsType"`
	ID    string  `json:"credentialsId"`
	Value *string `json:"credentialsValue,omitempty"`
}

// Record is one exported entity with everything exported alongside it.
type Record struct {
	EntityType  string                 `json:"entityType"`
	Entity      json.RawMessage        `json:"entity"`
	Relations   []Relation             `json:"relations,omitempty"`
	Attributes  map[string][]Attribute `json:"attributes,omitempty"`
	Credentials *Credentials           `json:"credentials,omitempty"`
}

// Entity is the union of the entity members this spike reads. Members a kind does not have are
// simply absent.
type Entity struct {
	ID             EntityID        `json:"id"`
	Name           string          `json:"name"`
	Type           string          `json:"type"`
	Label          string          `json:"label"`
	Description    string          `json:"description"`
	TransportType  string          `json:"transportType"`
	DeviceProfile  *EntityID       `json:"deviceProfileId"`
	AssetProfile   *EntityID       `json:"assetProfileId"`
	Customer       *EntityID       `json:"customerId"`
	CreatedTime    int64           `json:"createdTime"`
	AdditionalInfo json.RawMessage `json:"additionalInfo,omitempty"`
	ProfileData    *ProfileData    `json:"profileData,omitempty"`
}

// ProfileData is the part of a device profile this spike inspects: its alarm rules, which are
// classified in the report and not converted.
type ProfileData struct {
	Alarms []json.RawMessage `json:"alarms"`
}

// Export is a whole input: every record found.
type Export struct {
	Records []Record
}

// Load reads path, which is a JSON file or a directory of them. A file may hold one record, an
// array of records, or {"entities": [...]}.
func Load(path string) (*Export, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	files := []string{path}
	if st.IsDir() {
		files, err = filepath.Glob(filepath.Join(path, "*.json"))
		if err != nil {
			return nil, err
		}
		sort.Strings(files)
		if len(files) == 0 {
			return nil, fmt.Errorf("%s holds no .json files", path)
		}
	}
	ex := &Export{}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		recs, err := Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		ex.Records = append(ex.Records, recs...)
	}
	return ex, nil
}

// Parse decodes one document into records.
func Parse(raw []byte) ([]Record, error) {
	raw = []byte(strings.TrimSpace(string(raw)))
	if len(raw) == 0 {
		return nil, errors.New("empty document")
	}
	var recs []Record
	switch raw[0] {
	case '[':
		if err := json.Unmarshal(raw, &recs); err != nil {
			return nil, err
		}
	case '{':
		var wrapped struct {
			Entities []Record `json:"entities"`
		}
		if err := json.Unmarshal(raw, &wrapped); err != nil {
			return nil, err
		}
		if wrapped.Entities != nil {
			recs = wrapped.Entities
		} else {
			var one Record
			if err := json.Unmarshal(raw, &one); err != nil {
				return nil, err
			}
			recs = []Record{one}
		}
	default:
		return nil, errors.New("not a JSON object or array")
	}
	for i, r := range recs {
		if r.EntityType == "" || len(r.Entity) == 0 {
			return nil, fmt.Errorf("record %d has no entityType or entity", i+1)
		}
	}
	return recs, nil
}

func (r Record) entity() (Entity, error) {
	var e Entity
	err := json.Unmarshal(r.Entity, &e)
	return e, err
}
