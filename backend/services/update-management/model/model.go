// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"

	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// Artifact kinds: what an update artifact is.
const (
	KindFirmware      = "FIRMWARE"
	KindSoftware      = "SOFTWARE"
	KindConfiguration = "CONFIGURATION"
)

// Kinds is the artifact-kind vocabulary, in documentation order.
var Kinds = []string{KindFirmware, KindSoftware, KindConfiguration}

// Artifact lifecycle states.
const (
	// StateUploading: the metadata is declared and no bytes are accepted yet (or an
	// earlier upload failed verification). The only state in which content can be
	// uploaded.
	StateUploading = "UPLOADING"
	// StateAvailable: the bytes are stored and match the declared size and digest. The
	// artifact is immutable from here on.
	StateAvailable = "AVAILABLE"
	// StateRetired: withdrawn from new use. Its bytes and metadata remain.
	StateRetired = "RETIRED"
)

// TrustUnverified is the only trust status an artifact can carry. Nothing in this
// service authenticates an artifact's origin or checks a signature, and a value that
// could never be emitted would be a promise the code does not keep, so there is no
// VERIFIED to compare against.
const TrustUnverified = "UNVERIFIED"

// BlobPurpose is the object-store purpose segment artifact bytes are stored under:
// {instance}/{tenant}/artifacts/{id}.
const BlobPurpose = "artifacts"

// DigestPrefix is the algorithm tag every stored digest carries.
const DigestPrefix = "sha256:"

// Artifact is a tenant-scoped, IMMUTABLE update artifact: the declared metadata of a
// firmware, software or configuration image and a reference to its bytes in the object
// store.
//
// Immutable means the identity (name, component, version) names one binary for ever:
// once the content is AVAILABLE nothing about it changes, and a corrected build is a new
// version. The only transitions are UPLOADING to AVAILABLE (a verified upload) and
// either of those to RETIRED.
//
// Field order mirrors the migration's snapshot struct; physical column order is part of
// the schema.
type Artifact struct {
	gorm.Model
	rdb.TenantScoped
	rdb.TokenReference

	// Kind is FIRMWARE, SOFTWARE or CONFIGURATION.
	Kind string `gorm:"not null;size:16"`
	// Name, Component and Version are the artifact's identity, unique per tenant.
	Name      string `gorm:"not null;size:128"`
	Version   string `gorm:"not null;size:64"`
	Component string `gorm:"not null;size:64"`
	// SizeBytes is the declared size; an upload that delivers any other number of bytes
	// is refused.
	SizeBytes int64 `gorm:"not null"`
	// Digest is "sha256:" and 64 lowercase hex digits. It is a STRING column on purpose:
	// it is compared as bytes, and a jsonb column would re-render it.
	Digest    string `gorm:"not null;size:71"`
	MediaType string `gorm:"not null;size:127"`
	// CompatibleProfiles is a non-empty JSON array of device-profile tokens the artifact
	// may be assigned to.
	CompatibleProfiles datatypes.JSON `gorm:"not null"`
	TrustStatus        string         `gorm:"not null;size:16"`
	// Provenance is {uploadedBy, source, notes}, at most MaxProvenanceBytes.
	Provenance datatypes.JSON
	// BlobRef locates the stored bytes; null until the artifact is AVAILABLE.
	BlobRef sql.NullString `gorm:"size:512"`
	State   string         `gorm:"not null;size:16"`
	// UploadingSince is the lease of the one upload allowed in flight for this artifact:
	// the moment it was claimed, or null when none is. It is a column rather than process
	// memory because the service can run more than one replica, and two replicas each
	// holding a private "one upload at a time" would admit two.
	UploadingSince sql.NullTime
}

// DefaultOrder implements rdb.Sortable: newest first, token as the tiebreak so the
// order is total (created_at is not unique).
func (Artifact) DefaultOrder() string {
	return "artifacts.created_at DESC, artifacts.token ASC"
}

// ArtifactCreateRequest declares a new artifact. No bytes travel with it.
type ArtifactCreateRequest struct {
	Token              string
	Kind               string
	Name               string
	Version            string
	Component          string
	SizeBytes          int64
	Digest             string
	MediaType          *string
	CompatibleProfiles []string
	Source             *string
	Notes              *string
}

// ArtifactSearchCriteria filters and paginates an artifact search.
type ArtifactSearchCriteria struct {
	rdb.Pagination
	Kind      *string
	State     *string
	Component *string
}

// ArtifactSearchResults is a page of artifacts plus its pagination info.
type ArtifactSearchResults struct {
	Results    []Artifact
	Pagination rdb.SearchResultsPagination
}
