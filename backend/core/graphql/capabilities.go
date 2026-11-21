// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"encoding/json"
	"net/http"
)

const schemaCapabilityMediaType = "application/vnd.devicechain.schema+json"

func isSchemaCapabilityRequest(r *http.Request) bool {
	return r.Method == http.MethodOptions && r.Header.Get("Accept") == schemaCapabilityMediaType
}

// serveSchemaCapabilities runs only after the handler's normal bearer validation.
// The caller gets the contract for this exact endpoint and plane, without enabling
// anonymous introspection or executing a GraphQL document. No credentials or data
// are part of a schema contract. The format is independent of the release number.
func (h *HttpHandler) serveSchemaCapabilities(w http.ResponseWriter) {
	w.Header().Set("Content-Type", schemaCapabilityMediaType)
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(struct {
		Format int    `json:"format"`
		Schema string `json:"schema"`
	}{Format: 1, Schema: h.Schema.capabilitySchema})
}
