// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"io/fs"
	"net/http"
)

// presentationConfig is what web/index.html fetches on load to learn how to
// reach the platform: the subscribe leg of the Seam D presentation descriptor
// (sim-subsystem-contract.md §3, Seam D). tenant/manifestId are shown in the
// page header; wsUrl+token drive the graphql-ws connection.
type presentationConfig struct {
	Tenant     string `json:"tenant"`
	ManifestId string `json:"manifestId"`
	WsUrl      string `json:"wsUrl"`
	Token      string `json:"token"`
}

// RegisterPresentation mounts the static presentation page (served from
// webFS, rooted at "web/") and its /config.json endpoint on mux. The token is
// resolved fresh on every /config.json request so a page opened well after
// process start still gets a live (non-expired) access token; the page itself
// does not refresh it mid-session (slice-1 scope — see README).
func RegisterPresentation(mux *http.ServeMux, webFS fs.FS, rt *Runtime, manifestId string) {
	mux.Handle("GET /", http.FileServerFS(webFS))
	mux.HandleFunc("GET /config.json", func(w http.ResponseWriter, r *http.Request) {
		token, err := rt.Session.AccessToken(r.Context())
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, presentationConfig{
			Tenant:     rt.Tenant,
			ManifestId: manifestId,
			WsUrl:      rt.Endpoints.EventMgmtWS,
			Token:      token,
		})
	})
}
