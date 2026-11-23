// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"encoding/json"
	"net/http"
)

// Format 1 of this media type is permanent. Deployed clients (dcctl, the console, embedders on
// @devicechain/client) read exactly {"format":1,"schema":<SDL>} and refuse any other format, so a
// change to the manifest shape is a NEW media type, never a new "format" value served here.
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
