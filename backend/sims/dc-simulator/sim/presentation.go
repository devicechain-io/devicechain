// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"io/fs"
	"net/http"
)

// presentationConfig is what a presentation client fetches on load to learn how to
// reach the platform: tenant/manifestId identify the scenario, wsUrl+token drive the
// graphql-ws subscribe, and the mqtt* fields let an out-of-process client BE the
// device far end (FarEndExternal) rather than only watch one.
//
// It is the subscribe leg of the Seam D presentation descriptor, and it does NOT have
// the shape the contract doc (sim-subsystem-contract.md §3, Seam D) draws. That
// descriptor is nested — `{ url, launchHint, handshake: { controlApi, subscribe: {
// wsUrl, token } } }` — and this is flat, because the client fetching it is already
// talking to the process that serves it: the url and launchHint describe how to REACH
// this page, which is not information the page needs, and controlApi is the origin the
// fetch came from. The divergence is stated here rather than fixed because web/index.html
// and the sitepulse player both read the flat keys; the doc describes a superset seam,
// not this endpoint's body.
//
// 🔴 THE JSON TAGS ARE THE CONTRACT. The consumers are a plain HTML page and an
// out-of-process Unity player, neither of which can see a Go field name — and a renamed
// tag compiles cleanly and passes any test that decodes through this struct, so
// TestPresentationConfigWireNamesAreTheContract decodes the real body into a map and
// asserts the literal key strings — and asserts the key set is EXACTLY those, which is
// what makes the paragraph below a gate rather than an opinion.
//
// 🔴 WHAT IS DELIBERATELY ABSENT: device credentials. A FarEndExternal client needs a
// per-device bearer to open its own MQTT session, and the obvious "fix" is to serve
// rt.Devices here. That was considered and rejected on four independent grounds, any
// one of which is disqualifying:
//
//  1. This endpoint has NO AUTHENTICATION AT ALL, and what it already hands out is
//     larger than it looks. The sim identity is provisioned with the tenant-admin role
//     (cli/sim/admin.go), so the token in this body is not a scoped reader — it is full
//     create/read/delete over devices, credentials and dashboards across the tenant. Its
//     one saving grace is that it expires in minutes. Its second is that the bind
//     defaults to 127.0.0.1; main.go's flag help is where an operator actually decides
//     to widen that, and it names what is on the other side.
//     Note what this body ALREADY composes to for anyone who can reach it: mqttBroker +
//     instanceId + a tenant-admin token is everything needed to enumerate the fleet's
//     credentials and impersonate any device on the MQTT plane. That is the honest
//     reading, and the conclusion it forces is to add NOTHING PERMANENT here — not to
//     relax because the door is already open. Device credentials are ACCESS_TOKEN
//     bearers derived from the manifest seed: permanent, non-rotating, one per device,
//     and unlike the token beside them they do not expire out of a stale page, a log, a
//     proxy cache or a screenshot.
//
//  2. It would go STALE. POST /reset re-runs Bootstrap, which re-provisions and
//     REASSIGNS rt.Devices; a config fetched before a reset would then be silently
//     wrong rather than visibly absent — the client would keep authenticating with
//     credentials for a cohort that no longer exists.
//
//  3. Reading rt.Devices from this handler is an UNSYNCHRONIZED read. Lifecycle's
//     bootstrapMu serializes WRITERS only; nothing here holds it, so a /config.json
//     served concurrently with a reset races the slice header the race detector would
//     be right to flag.
//
//  4. It is REDUNDANT, which is what makes the other three unnecessary to trade off.
//     Given (1) — the caller already holds a tenant-admin token — serving credentials
//     grants no capability the caller lacks; it only writes a permanent secret into a
//     body that an ephemeral one will vacate on its own. device-management exposes
//     credentialId as a readable field on DeviceCredential (only credentialValue is
//     withheld — its resolver returns nil unconditionally), and for an ACCESS_TOKEN
//     credential the bearer IS the credentialId, not the withheld value. So the client
//     resolves its own credentials over GraphQL and reads the DATABASE rather than a
//     snapshot taken at page load, which fixes (2) as a side effect.
//
//     Note what (1) is carrying here. The queries returning a DeviceCredential are
//     gated on device:WRITE, not device:read, precisely because credentialId is the
//     bearer for an ACCESS_TOKEN — so this round trip works because the token issued
//     by dcctl carries tenant-admin (seeded with the super-authority), not because
//     credentials are broadly readable. A presentation client handed a narrower
//     token would fail the second query, and the right answer then is a scoped
//     self-lookup, never widening the gate or reinstating (4).
//
// The round trip that (4) refers to, written down so it is discoverable from the file
// that declines to shortcut it — a client that knows a device by its scene-side business
// id resolves the addressing token first, then the credential:
//
//	devicesByExternalId(externalIds: ["SITE-EXCAVATOR-01"]) { token }
//	deviceCredentialsByToken(tokens: ["{token}-cred"])      { credentialId }
//
// The `-cred` suffix is not a convention the client may invent: it is what
// deriveCredential (manifest.go) provisions the DeviceCredential's own entity token as,
// and deviceCredentialsByToken looks up credentials by THAT token, not by the device's.
type presentationConfig struct {
	Tenant     string `json:"tenant"`
	ManifestId string `json:"manifestId"`
	WsUrl      string `json:"wsUrl"`
	Token      string `json:"token"`

	// InstanceId is what makes a client id the MQTT gateway will accept: its auth
	// callout REFUSES any client id but `{instance}:{tenant}:{deviceToken}`, so a
	// presentation client that cannot name the instance fails at CONNECT, before
	// anything it could diagnose from the scene.
	//
	// It is NOT a new disclosure, and the earlier version of this comment claiming it
	// had no second source was simply wrong: statusResponse (lifecycle.go:257) carries
	// the same `instanceId`, set from the same rt.InstanceId (lifecycle.go:304), and
	// GET /status is mounted on the SAME mux behind the SAME absent authentication —
	// as is `tenant`. Putting it here is about where a client should have to look: the
	// config it already fetches, rather than scraping the operator-facing control API
	// for a field that is not part of any contract with it.
	InstanceId string `json:"instanceId"`

	// MqttBroker is the gateway address. It is not derivable from wsUrl (a different
	// service, a different port, a different protocol), and like instanceId above it is
	// not a new disclosure: commandFarEndStatus already publishes the same rt.MqttBroker
	// as commandFarEnd.broker on GET /status (farend.go:231), on the same mux behind the
	// same absent authentication. The reason it is duplicated here is the same one — a
	// presentation client should read its own config, not parse the control API dcctl
	// owns — and the same caution applies: these are two views of one value, both from
	// rt.MqttBroker, so neither may be re-sourced independently.
	//
	// There is deliberately no empty-broker guard in the handler. attachCommandFarEnd
	// already REFUSES to bootstrap a FarEndExternal scenario whose rt.MqttBroker is
	// empty, unless the operator passed --no-command-far-end; so by the time anything
	// can GET this, an external scenario either has a broker or its operator explicitly
	// accepted an unanswered command channel. Re-checking here would add nothing and
	// take something away: a per-request 502 fires in the client's fetch, on another
	// machine, minutes after the misconfiguration, whereas the bootstrap gate fails in
	// the log the operator is already watching with an error naming the fix. A guard
	// belongs where it fails loudly, not where it fails distantly.
	MqttBroker string `json:"mqttBroker"`

	// MqttTLSInsecure is the operator's RECORDED trust decision, threaded from the
	// handshake so the player's TLS config matches what dcctl already printed — a
	// client that verified while the record said not to fails with an x509 error
	// contradicting the record, and a client that skipped verification unasked is
	// worse.
	//
	// 🔴 No omitempty, and that is the whole point of a separate line for it. A false
	// bool with omitempty vanishes from the body, and a client reading an ABSENT key
	// cannot distinguish "verify" from "this build does not send it" — for a trust
	// decision the two must not collapse, and the dangerous direction is exactly the
	// one omitempty picks. TestPresentationConfigKeepsFalseTLSInsecureOnTheWire asserts
	// the key is present and false.
	MqttTLSInsecure bool `json:"mqttTLSInsecure"`
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
			// The whole config is refused rather than served token-less. A body with an
			// empty token is a 200 the client believes, and it fails later as a
			// subscribe the platform rejects; a 502 fails where the cause is.
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, presentationConfig{
			Tenant:     rt.Tenant,
			ManifestId: manifestId,
			WsUrl:      rt.Endpoints.EventMgmtWS,
			Token:      token,
			// 🔴 The flattened Runtime fields, not rt.Endpoints.*: MqttTLSInsecure lives
			// only on Runtime (it is a Handshake field, not an Endpoints one), and every
			// other consumer in this module — attachCommandFarEnd, commandFarEndStatus —
			// reads rt.MqttBroker, so reading the nested copy here would be a second
			// source that can disagree with the one the far end actually dials.
			InstanceId:      rt.InstanceId,
			MqttBroker:      rt.MqttBroker,
			MqttTLSInsecure: rt.MqttTLSInsecure,
		})
	})
}
