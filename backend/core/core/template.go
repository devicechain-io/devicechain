// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
)

// templatePlaceholder matches the identifier-template placeholders the platform
// understands: "{n}" (the 1-based index, unpadded), "{n:0Wd}" (the index
// zero-padded to width W), and "{random}" (a fresh random token). It lives in the
// leaf core package so the SAME grammar renders a bulk-provisioning template on
// the server (device-management's createDevices) and in any client that predicts
// the resulting tokens (the sim's population Expand), rather than two drift-prone
// copies.
var templatePlaceholder = regexp.MustCompile(`\{n(?::0(\d+)d)?\}|\{random\}`)

// indexPlaceholder matches only the index forms ("{n}"/"{n:0Wd}"), used to check
// a template will actually vary per instance.
var indexPlaceholder = regexp.MustCompile(`\{n(?::0(\d+)d)?\}`)

const (
	// MaxTemplateLen bounds a template's own length. A generous ceiling — a real
	// token/name template is a handful of characters — that still stops a caller
	// from submitting a megabyte of template text.
	MaxTemplateLen = 256

	// MaxTemplatePadWidth bounds a "{n:0Wd}" pad width. Without a bound, fmt's
	// "%0*d" renders a width-1,000,000 index into a 1 MB string for a ~13-byte
	// placeholder — a memory-amplification vector the moment a template is rendered
	// across a whole batch (count × occurrences). It is capped at MaxTokenLen: a
	// pad wider than the longest storable token could never yield a usable token
	// anyway, so nothing legitimate wants more.
	MaxTemplatePadWidth = MaxTokenLen
)

// ValidateTemplate checks a template is safe to render repeatedly: bounded length
// and every "{n:0Wd}" pad width within MaxTemplatePadWidth. It validates the
// TEMPLATE, not the rendered output — grammar and per-index length are the caller's
// job (they depend on the index) — its single purpose is to keep rendering from
// being turned into a memory-amplification attack. A template with no placeholders
// is valid (it renders a constant).
func ValidateTemplate(template string) error {
	if len(template) > MaxTemplateLen {
		return fmt.Errorf("template exceeds the maximum length of %d characters", MaxTemplateLen)
	}
	for _, m := range indexPlaceholder.FindAllStringSubmatch(template, -1) {
		if m[1] == "" {
			continue // "{n}" — unpadded, no width to bound
		}
		width, err := strconv.Atoi(m[1])
		if err != nil {
			continue // width overflows int; RenderTemplate falls back to unpadded (small), not a threat
		}
		if width > MaxTemplatePadWidth {
			return fmt.Errorf("template pad width %d exceeds the maximum of %d", width, MaxTemplatePadWidth)
		}
	}
	return nil
}

// RenderTemplate renders an identifier template for a single instance:
//
//   - each "{n}" / "{n:0Wd}" is replaced with index (1-based), unpadded or
//     zero-padded to the given width;
//   - each "{random}" is replaced with a fresh value from randToken.
//
// randToken is called once PER "{random}" occurrence, so N occurrences yield N
// independent values. Passing a nil randToken leaves "{random}" as a literal —
// callers that must stay deterministic (a client predicting a server-rendered
// token) pass nil and simply never use "{random}" in a template whose output they
// need to reproduce. With no "{random}" placeholder the result is a pure function
// of (template, index), which is what makes a token template reproducible on both
// sides of the wire.
func RenderTemplate(template string, index int, randToken func() string) string {
	return templatePlaceholder.ReplaceAllStringFunc(template, func(match string) string {
		if match == "{random}" {
			if randToken == nil {
				return match
			}
			return randToken()
		}
		sub := indexPlaceholder.FindStringSubmatch(match)
		if sub == nil || sub[1] == "" {
			return strconv.Itoa(index)
		}
		width, err := strconv.Atoi(sub[1])
		if err != nil {
			return strconv.Itoa(index)
		}
		// Defensive cap: an unbounded "%0*d" width is a memory-amplification vector
		// (a width-1e6 pad renders a 1 MB string). Callers that accept untrusted
		// templates should ValidateTemplate first for a clear error; this clamp is
		// the backstop so rendering itself can never blow up, whatever the caller.
		if width > MaxTemplatePadWidth {
			width = MaxTemplatePadWidth
		}
		return fmt.Sprintf("%0*d", width, index)
	})
}

// HasIndexPlaceholder reports whether template contains an index placeholder
// ("{n}" or "{n:0Wd}") — i.e. whether it will render a DISTINCT string per index.
// A bulk create uses this to reject a token template that would render the same
// token for every device (a whole batch colliding on one unique key) with a clear
// message instead of an opaque duplicate-key error deep in the transaction.
func HasIndexPlaceholder(template string) bool {
	return indexPlaceholder.MatchString(template)
}

// RandomHexToken returns 8 bytes of cryptographic randomness as 16 lowercase hex
// characters — always token-grammar-safe (ValidateToken), suitable for filling a
// "{random}" placeholder in an external-id template so a provisioned fleet's
// business ids scatter across the key space instead of clustering sequentially.
// It panics only if the system CSPRNG fails, which Go treats as unrecoverable.
func RandomHexToken() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		panic(fmt.Sprintf("crypto/rand read failed: %v", err))
	}
	return hex.EncodeToString(buf)
}
