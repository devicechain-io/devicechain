// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"errors"
	"fmt"

	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/semantic"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// MaxSemanticTypes bounds how many classes one type may be bound to. Real assets are
// multi-typed (a luminaire is a streetlight to one consumer and a luminaire to another), but
// a list that grows without limit is a document nobody reviews.
const MaxSemanticTypes = 8

// CodeInvalidArgument is the extensions.code of a refused semantic binding or context: the
// request named a term, pack or prefix the vocabulary layer does not accept. The offending
// term rides extensions.term when there is one.
const CodeInvalidArgument = "INVALID_ARGUMENT"

// CodeSemanticContextInUse is the extensions.code of a setSemanticContext refused because
// bindings already stored would not be explained by the context it asked for (a pack it
// stops enabling, a custom namespace it stops declaring). extensions.references carries how
// many bindings; the request is well formed, and becomes acceptable once they are unbound.
const CodeSemanticContextInUse = "SEMANTIC_CONTEXT_IN_USE"

// SemanticContext is a tenant's semantic configuration (one live row per tenant): which
// vocabulary packs it has enabled, the custom prefixes it declared for its own terms, and
// the version of each enabled pack recorded when the context was last set.
//
// Bindings store EXPANDED IRIs, never CURIEs, so nothing here is read back to interpret a
// stored binding: remapping a custom prefix changes how a binding is DISPLAYED and never what
// it is.
type SemanticContext struct {
	gorm.Model
	rdb.TenantScoped

	// EnabledPacks are pack ids, sorted.
	EnabledPacks rdb.Anchors `gorm:"type:text[];not null"`
	// CustomPrefixes is {prefix: namespace}.
	CustomPrefixes datatypes.JSON `gorm:"not null"`
	// PackVersions is {pack id: version} as this build shipped the pack when the context was
	// set.
	PackVersions datatypes.JSON `gorm:"not null"`
}

// SemanticContextRequest replaces a tenant's semantic context whole.
type SemanticContextRequest struct {
	EnabledPacks   []string
	CustomPrefixes *[]CustomPrefixInput
}

// CustomPrefixInput declares one custom prefix and the namespace it stands for.
type CustomPrefixInput struct {
	Prefix    string
	Namespace string
}

// CustomPrefix is a declared custom prefix as it is served.
type CustomPrefix struct {
	Prefix    string
	Namespace string
}

// PackVersion is one enabled pack's recorded version.
type PackVersion struct {
	Pack    string
	Version string
}

// SemanticError is a refused binding or context. It carries INVALID_ARGUMENT as its code (or
// SEMANTIC_CONTEXT_IN_USE for a context that stored bindings still need) and the offending
// term, when it has one, so a client does not have to match prose.
//
// graphql-go copies a resolver error's Extensions() only by direct type assertion, so the
// API returns this type unwrapped.
type SemanticError struct {
	// Field names what was being bound ("semanticTypes", "semanticPredicate", ...).
	Field string
	Err   error
	// References is how many bindings still use a pack or namespace a context change would
	// have dropped; zero otherwise.
	References int
	// Code overrides the extensions.code; empty means CodeInvalidArgument.
	Code string
}

func (e *SemanticError) Error() string {
	if e.Field == "" {
		return e.Err.Error()
	}
	return fmt.Sprintf("%s: %v", e.Field, e.Err)
}

func (e *SemanticError) Unwrap() error { return e.Err }

// Extensions implements the graphql-go error extensions contract.
func (e *SemanticError) Extensions() map[string]any {
	code := e.Code
	if code == "" {
		code = CodeInvalidArgument
	}
	ext := map[string]any{"code": code}
	var te *semantic.TermError
	if errors.As(e.Err, &te) && te.Term != "" {
		ext["term"] = te.Term
	}
	if e.References > 0 {
		ext["references"] = e.References
	}
	return ext
}

func semanticRefusal(field string, err error) error {
	return &SemanticError{Field: field, Err: err}
}
