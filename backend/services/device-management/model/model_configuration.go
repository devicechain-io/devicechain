// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/datatypes"
)

// MaxConfigurationDocumentBytes caps a device's configuration document (its canonical
// JSON encoding). It is a FIXED platform ceiling: a SHARED attribute write that would push
// a device's document past it is refused at the write, never discovered at delivery.
const MaxConfigurationDocumentBytes = 64 * 1024

// ConfigurationDigestPrefix names the digest algorithm in every stored digest.
const ConfigurationDigestPrefix = "sha256:"

// ConfigurationStatus is the vocabulary of a device's configuration report.
type ConfigurationStatus string

const (
	// ConfigurationReceived: the device holds the revision but has not applied it yet.
	ConfigurationReceived ConfigurationStatus = "RECEIVED"
	// ConfigurationApplied: the device applied the revision.
	ConfigurationApplied ConfigurationStatus = "APPLIED"
	// ConfigurationRejected: the device refused the revision; errors say why.
	ConfigurationRejected ConfigurationStatus = "REJECTED"
)

// Refusal codes carried on extensions.code by a refused attribute write.
const (
	// CodeConfigurationTooLarge: the write would push a device's configuration document
	// past MaxConfigurationDocumentBytes.
	CodeConfigurationTooLarge = "CONFIGURATION_TOO_LARGE"
	// CodeConfigurationValueType: the write stores a declared configuration key with a
	// value type, or a value, that the declaration's value type cannot carry.
	CodeConfigurationValueType = "CONFIGURATION_VALUE_TYPE_MISMATCH"
)

// ConfigurationRefusal is a refused attribute write, with its code on extensions.code.
type ConfigurationRefusal struct {
	Code    string
	Message string
}

func (e *ConfigurationRefusal) Error() string { return e.Message }

// Extensions implements graphql-go's extensions hook so the code reaches the client.
func (e *ConfigurationRefusal) Extensions() map[string]any {
	return map[string]any{"code": e.Code}
}

// DeviceConfigurationRevision is one immutable revision of a device's device-visible
// configuration document. Append-only: there is no update or delete entry point for it
// in the API or the GraphQL schema; rows leave only with their device or their tenant.
//
// The document is the canonical JSON the digest is computed over, stored as those exact
// bytes, so a reader is handed precisely what the digest covers.
type DeviceConfigurationRevision struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time

	rdb.TenantScoped

	DeviceId uint  `gorm:"not null;uniqueIndex:uix_dcr_device_revision,priority:1"`
	Revision int64 `gorm:"not null;uniqueIndex:uix_dcr_device_revision,priority:2"`

	// ProfileVersionId is the published profile version whose declaration built this
	// document; nil when the device resolves no published profile (an empty document).
	ProfileVersionId *uint

	Document datatypes.JSON `gorm:"not null"`
	Digest   string         `gorm:"not null;size:71"`
	// Actor is the authenticated subject whose write caused the revision.
	Actor string `gorm:"size:256"`
}

// DefaultOrder implements rdb.Sortable: newest revision first, and revision is unique
// per device; id breaks ties across devices.
func (DeviceConfigurationRevision) DefaultOrder() string {
	return "device_configuration_revisions.revision DESC, device_configuration_revisions.id DESC"
}

// AuditLabel names the device and revision in the audit journal by internal id, never by
// the customer-chosen device token.
func (r DeviceConfigurationRevision) AuditLabel() string {
	return fmt.Sprintf("device #%d revision %d", r.DeviceId, r.Revision)
}

// DeviceConfigurationState is the device's last reported configuration state: one row per
// device. Nothing in this slice writes it from a request — it is written only by the
// device-report path — so there is no GraphQL mutation for it.
type DeviceConfigurationState struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time

	rdb.TenantScoped

	DeviceId uint `gorm:"not null;uniqueIndex:uix_dcs_device"`

	ReportedRevision sql.NullInt64
	ReportedDigest   sql.NullString `gorm:"size:71"`
	ReportedStatus   sql.NullString `gorm:"size:16"`
	ReportedErrors   datatypes.JSON
	ReportedAt       sql.NullTime
	LastSyncAt       sql.NullTime
}

// ReportedErrorList decodes the stored error list; a missing or unreadable value is an
// empty list, never nil.
func (s *DeviceConfigurationState) ReportedErrorList() []string {
	out := make([]string, 0)
	if len(s.ReportedErrors) == 0 {
		return out
	}
	_ = json.Unmarshal(s.ReportedErrors, &out)
	if out == nil {
		out = make([]string, 0)
	}
	return out
}

// DeviceConfiguration is the read model of one device's configuration.
type DeviceConfiguration struct {
	// Desired is the latest minted revision, or nil when none has been minted.
	Desired *DeviceConfigurationRevision
	// Reported is the device's last report, or nil when it has never reported.
	Reported *DeviceConfigurationState
	// Pending: a desired revision exists and the device has not reported APPLIED for
	// that same revision AND that same digest.
	Pending bool
	// Stale: the document the device's current declaration and SHARED values would build
	// differs from the latest minted revision (for example, after a profile publish).
	Stale bool
	// Undeclared lists the device's SHARED attribute keys its declaration does not name.
	// They are never part of the document.
	Undeclared []string
	// Invalid lists declared keys whose stored value cannot be carried as the declared
	// value type. They are omitted from the document.
	Invalid []string
}

// IsPending decides convergence by identity, not by value: the device is converged only
// when it reported APPLIED for the desired revision with the desired digest.
func IsPending(desired *DeviceConfigurationRevision, reported *DeviceConfigurationState) bool {
	if desired == nil {
		return false
	}
	if reported == nil || !reported.ReportedStatus.Valid || !reported.ReportedRevision.Valid ||
		!reported.ReportedDigest.Valid {
		return true
	}
	return !(ConfigurationStatus(reported.ReportedStatus.String) == ConfigurationApplied &&
		reported.ReportedRevision.Int64 == desired.Revision &&
		reported.ReportedDigest.String == desired.Digest)
}

// ConfigurationDocument is a device-visible configuration document as built from a
// declaration and a device's SHARED attributes.
type ConfigurationDocument struct {
	// Canonical is the canonical JSON encoding; Digest is computed over exactly it.
	Canonical []byte
	Digest    string
	// Undeclared and Invalid are as on DeviceConfiguration, sorted.
	Undeclared []string
	Invalid    []string
}

// ConfigurationDigest is "sha256:" + the lowercase hex SHA-256 of the canonical bytes.
func ConfigurationDigest(canonical []byte) string {
	sum := sha256.Sum256(canonical)
	return ConfigurationDigestPrefix + hex.EncodeToString(sum[:])
}

// BuildConfigurationDocument builds a device's configuration document from the declared
// keys and the device's SHARED attributes ONLY. The caller passes SHARED attributes; any
// attribute of another scope is ignored here as well, so a SERVER or CLIENT value can
// never reach a device through this function whatever it is handed.
//
// The document is a JSON object of declared key → typed value, keys sorted:
//   - LONG → a JSON integer (exact, int64);
//   - DOUBLE → the shortest round-tripping number (ECMAScript formatting, so 22.0 is 22);
//   - BOOLEAN → true/false;
//   - STRING → a JSON string;
//   - JSON → the value re-encoded canonically (object keys sorted, numbers as DOUBLE).
//
// A declared key with no SHARED value, or with a null value, is omitted. A declared key
// whose stored value type differs from the declared one, or whose value does not parse as
// it, is omitted and listed in Invalid. SHARED keys that are not declared are listed in
// Undeclared and never sent.
func BuildConfigurationDocument(declared []ConfigurationKey, attrs []*EntityAttribute) (*ConfigurationDocument, error) {
	shared := make(map[string]*EntityAttribute, len(attrs))
	for _, a := range attrs {
		if a == nil || a.Scope != string(AttributeScopeShared) {
			continue
		}
		shared[a.AttrKey] = a
	}
	declaredSet := make(map[string]ConfigurationKey, len(declared))
	for _, k := range declared {
		declaredSet[k.Key] = k
	}

	doc := &ConfigurationDocument{Undeclared: make([]string, 0), Invalid: make([]string, 0)}
	for key := range shared {
		if _, ok := declaredSet[key]; !ok {
			doc.Undeclared = append(doc.Undeclared, key)
		}
	}
	sort.Strings(doc.Undeclared)

	keys := make([]string, 0, len(declaredSet))
	for key := range declaredSet {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var buf bytes.Buffer
	buf.WriteByte('{')
	first := true
	for _, key := range keys {
		attr, ok := shared[key]
		if !ok || !attr.Value.Valid {
			continue
		}
		decl := declaredSet[key]
		value, err := canonicalConfigurationValue(decl.ValueType, attr.ValueType, attr.Value.String)
		if err != nil {
			doc.Invalid = append(doc.Invalid, key)
			continue
		}
		if !first {
			buf.WriteByte(',')
		}
		first = false
		if err := writeCanonicalString(&buf, key); err != nil {
			return nil, err
		}
		buf.WriteByte(':')
		buf.Write(value)
	}
	buf.WriteByte('}')
	doc.Canonical = buf.Bytes()
	doc.Digest = ConfigurationDigest(doc.Canonical)
	return doc, nil
}

var errConfigurationValue = errors.New("value cannot be carried as the declared type")

// canonicalConfigurationValue renders one stored value as canonical JSON for the declared
// type. The stored type must equal the declared type: a value written as STRING is not
// silently reinterpreted as a number because a declaration later said LONG.
func canonicalConfigurationValue(declaredType, storedType, text string) ([]byte, error) {
	if declaredType != storedType {
		return nil, errConfigurationValue
	}
	switch AttributeValueType(declaredType) {
	case AttributeValueLong:
		i, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			return nil, errConfigurationValue
		}
		return []byte(strconv.FormatInt(i, 10)), nil
	case AttributeValueDouble:
		f, err := strconv.ParseFloat(text, 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, errConfigurationValue
		}
		return []byte(configurationNumber(f)), nil
	case AttributeValueBoolean:
		b, err := strconv.ParseBool(text)
		if err != nil {
			return nil, errConfigurationValue
		}
		return []byte(strconv.FormatBool(b)), nil
	case AttributeValueString:
		var buf bytes.Buffer
		if err := writeCanonicalString(&buf, text); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	case AttributeValueJson:
		dec := json.NewDecoder(strings.NewReader(text))
		dec.UseNumber()
		var v any
		if err := dec.Decode(&v); err != nil {
			return nil, errConfigurationValue
		}
		if _, err := dec.Token(); err != io.EOF {
			return nil, errConfigurationValue
		}
		var buf bytes.Buffer
		if err := writeCanonicalJSON(&buf, v); err != nil {
			return nil, errConfigurationValue
		}
		return buf.Bytes(), nil
	default:
		return nil, errConfigurationValue
	}
}

// configurationNumber formats a finite float64 the way ECMAScript's Number#toString does
// (the JSON canonicalization scheme's number form): the shortest digits that round-trip,
// plain decimal for 1e-6 <= |f| < 1e21, exponent form otherwise, and -0 as 0.
func configurationNumber(f float64) string {
	if f == 0 {
		return "0"
	}
	abs := math.Abs(f)
	if abs >= 1e-6 && abs < 1e21 {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	s := strconv.FormatFloat(f, 'e', -1, 64)
	mantissa, exp, _ := strings.Cut(s, "e")
	sign, digits := exp[:1], strings.TrimLeft(exp[1:], "0")
	return mantissa + "e" + sign + digits
}

func writeCanonicalString(buf *bytes.Buffer, s string) error {
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return err
	}
	buf.Truncate(buf.Len() - 1) // Encode appends a newline
	return nil
}

func writeCanonicalJSON(buf *bytes.Buffer, v any) error {
	switch t := v.(type) {
	case nil:
		buf.WriteString("null")
	case bool:
		buf.WriteString(strconv.FormatBool(t))
	case json.Number:
		f, err := strconv.ParseFloat(t.String(), 64)
		if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
			return errConfigurationValue
		}
		buf.WriteString(configurationNumber(f))
	case string:
		return writeCanonicalString(buf, t)
	case []any:
		buf.WriteByte('[')
		for i, e := range t {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeCanonicalJSON(buf, e); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		buf.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeCanonicalString(buf, k); err != nil {
				return err
			}
			buf.WriteByte(':')
			if err := writeCanonicalJSON(buf, t[k]); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	default:
		return errConfigurationValue
	}
	return nil
}
