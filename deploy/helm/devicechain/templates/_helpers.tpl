{{/*
Copyright The DeviceChain Authors
SPDX-License-Identifier: Apache-2.0
*/}}

{{/*
devicechain.enabledAreas resolves the deployment selection (a named profile or an
explicit set, defaulting to the default profile) and validates it against the
ADR-022 decision-2 dependency rules, then returns the enabled functional areas as
a comma-joined string. It FAILS the render on an invalid selection.

The catalog below mirrors backend/k8s/functionalarea (the Go source of truth that
the operator uses); keep the two in sync. Soft dependencies are intentionally not
encoded — pub/sub (ADR-003) makes an absent peer safe, so only hard edges gate.
*/}}
{{/*
devicechain.areasWithoutPublicApi — functional areas that serve NO external API.

They register only /metrics and the probes on the default mux: no GraphQL schema,
no bearer-token auth gate. Giving one a public /api/<area> route therefore does not
expose an API — it exposes an UNAUTHENTICATED PROMETHEUS ENDPOINT, which leaks
device and tenant counts, error rates and broker topology to anyone who can reach
the ingress host.

🔴 This is a LIST rather than an inline name check because it was a name check, and
the second such area was added without it. Adding an area here is the one step that
keeps a metrics-only service off the public router; if you add an ingest-style area
that serves no schema, add it here in the same commit.
*/}}
{{- define "devicechain.areasWithoutPublicApi" -}}
sparkplug-ingest,lwm2m-ingest
{{- end }}

{{- define "devicechain.enabledAreas" -}}
  {{- $standard := list "user-management" "device-management" "event-sources" "event-management" "device-state" "dashboard-management" "command-delivery" "notification-management" "event-processing" -}}
  {{- $profiles := dict
      "default"     $standard
      "full"        (concat $standard (list "ai-inference" "outbound-connectors" "mcp" "sparkplug-ingest" "lwm2m-ingest" "update-management"))
      "telemetry"   (list "user-management" "device-management" "event-sources" "event-management" "device-state" "dashboard-management")
      "ingest-only" (list "user-management" "device-management" "event-sources")
  -}}
  {{- $core := list "user-management" "device-management" -}}
  {{- $hard := dict
      "event-sources"        (list "device-management")
      "event-management"     (list "device-management")
      "device-state"         (list "device-management")
      "dashboard-management" (list "device-management")
      "command-delivery"     (list "device-management")
      "notification-management" (list "device-management")
      "event-processing"     (list "device-management")
      "outbound-connectors"  (list "event-processing")
      "mcp"                  (list "device-management")
      "sparkplug-ingest"     (list "device-management")
      "lwm2m-ingest"         (list "device-management")
      "update-management"    (list "device-management")
  -}}
  {{- $known := list "user-management" "device-management" "event-sources" "event-management" "device-state" "dashboard-management" "command-delivery" "notification-management" "event-processing" "outbound-connectors" "mcp" "ai-inference" "sparkplug-ingest" "lwm2m-ingest" "update-management" -}}

  {{- $profile := .Values.profile | default "" -}}
  {{- $explicit := .Values.enabledFunctionalAreas | default (list) -}}
  {{- $enabled := list -}}
  {{- if and (ne $profile "") (gt (len $explicit) 0) -}}
    {{- fail "devicechain: set either profile or enabledFunctionalAreas, not both" -}}
  {{- else if ne $profile "" -}}
    {{- $enabled = index $profiles $profile -}}
    {{- if not $enabled -}}
      {{- fail (printf "devicechain: unknown profile %q (known: default, full, telemetry, ingest-only)" $profile) -}}
    {{- end -}}
  {{- else if gt (len $explicit) 0 -}}
    {{- $enabled = $explicit -}}
  {{- else -}}
    {{- $enabled = index $profiles "default" -}}
  {{- end -}}

  {{- range $a := $enabled -}}
    {{- if not (has $a $known) -}}
      {{- fail (printf "devicechain: unknown functional area %q" $a) -}}
    {{- end -}}
  {{- end -}}
  {{- range $c := $core -}}
    {{- if not (has $c $enabled) -}}
      {{- fail (printf "devicechain: required core functional area %q is not enabled" $c) -}}
    {{- end -}}
  {{- end -}}
  {{- range $a := $enabled -}}
    {{- range $d := (index $hard $a | default (list)) -}}
      {{- if not (has $d $enabled) -}}
        {{- fail (printf "devicechain: functional area %q requires %q, which is not enabled" $a $d) -}}
      {{- end -}}
    {{- end -}}
  {{- end -}}

  {{- join "," $enabled -}}
{{- end -}}

{{/* The image reference for a functional area: per-area override or the default. */}}
{{- define "devicechain.image" -}}
  {{- $area := .area -}}
  {{- $root := .root -}}
  {{- $override := (get (.root.Values.functionalAreas | default dict) $area | default dict).image | default "" -}}
  {{- if $override -}}
    {{- $override -}}
  {{- else -}}
    {{- $tag := $root.Values.image.tag | default $root.Chart.AppVersion -}}
    {{- printf "%s/%s:%s" $root.Values.image.registry $area $tag -}}
  {{- end -}}
{{- end -}}

{{/*
The web console image reference: explicit frontend.image.repository:tag overrides,
otherwise "{image.registry}/frontend:{image.tag}" — same registry/tag the services
resolve through, so a release pins the whole deploy coherently.
*/}}
{{- define "devicechain.frontendImage" -}}
  {{- $fe := .Values.frontend | default dict -}}
  {{- $img := $fe.image | default dict -}}
  {{- $repo := $img.repository | default (printf "%s/frontend" .Values.image.registry) -}}
  {{- $tag := $img.tag | default .Values.image.tag | default .Chart.AppVersion -}}
  {{- printf "%s:%s" $repo $tag -}}
{{- end -}}

{{/*
THE namespace this instance's objects live in. Takes the ROOT context — several
templates call it from inside a `range`/`with`, so they pass the `$root` (or `$`)
they already hold rather than the dot in scope.

🔴 THIS IS THE ONLY PLACE THE INSTANCE'S NAMESPACE IS SPELLED. Every template
that needs it — every `metadata.namespace`, the Namespace object's own name, the
ServiceMonitor's namespaceSelector, the PodMonitor's in-namespace default, the
`namespace="…"` matcher in every alert expression, the dashboards' hidden
`namespace` constant — goes through here, and nothing writes it inline.

🔴 IT IS NOT THE INSTANCE ID, AND EVERY ONE OF THOSE SITES USED TO SAY THE ID.
The `dci-` prefix keeps an instance's namespace out of reach of the cluster's own
— monitoring, dc-system, cert-manager, cnpg-system, ingress-nginx — so an
instance named after one of them cannot be built into it. What a per-site
spelling would have left instead is a few dozen sites in which the ones that mean
something ELSE by the instance id are indistinguishable from the ones that mean
the namespace: the object names (`dci-<id>-config`, the `dc-<id>` ServiceAccount),
the `devicechain.io/instance` LABEL that is also a pod selector, the
`devicechain_instance` alert label that routing groups on, the Grafana uid, title,
ConfigMap name and folder, `DC_INSTANCE_ID` and everything the services derive
from it (database names, NATS subjects, MQTT topics). Those stay the id. Do not
route them through here, and do not write the namespace anywhere else.

🔴 THE `dci-` BELOW AND THE `dci-` IN `dci-<id>-config` ARE NOT THE SAME THING.
The second prefixes object NAMES and is older; `dci-acme-config` is not "the
namespace plus -config". dcctl spells this same prefix once, in
bootstrap/instancenamespace.go, and a test renders this chart and holds the two
answers equal — they are computed twice and must agree, because dcctl creates and
labels the namespace the chart then renders into.
*/}}
{{- define "devicechain.instanceNamespace" -}}
{{- printf "dci-%s" .Values.instance.id -}}
{{- end -}}

{{/*
The instance config Secret name. C2 (ADR-022 review): the instance config holds
persistence credentials, so it is rendered into a Secret (not a ConfigMap). When
instance.existingSecret is set, that name is used instead so an operator can point
at an External-Secrets-managed / pre-created Secret holding the `instance` key.
*/}}
{{- define "devicechain.instanceConfigSecret" -}}
{{- .Values.instance.existingSecret | default (printf "dci-%s-config" .Values.instance.id) -}}
{{- end -}}

{{/*
The Secret user-management reads its superuser's SEED password from (key `password`),
projected as DC_SUPERUSER_PASSWORD. dcctl bootstrap generates it per instance under
this default name; instance.superuserSecret overrides the name for an install that
manages the Secret itself.

It is deliberately a Secret of its own rather than a key in either config document:
the per-service document is a ConfigMap, and the instance document reaches every
service, while this value is one area's and is read only to seed an empty identity
table. The name is spelled in Go too (superuserSecretName in
backend/cli/bootstrap/superuser.go); TestTheChartReadsTheSuperuserSecretDcctlWrites
holds the two together.
*/}}
{{- define "devicechain.superuserSecret" -}}
{{- .Values.instance.superuserSecret | default (printf "dci-%s-superuser" .Values.instance.id) -}}
{{- end -}}

{{/* The per-service config ConfigMap name. */}}
{{- define "devicechain.microserviceConfigMap" -}}
{{- printf "dct-%s-config" .Values.instance.id -}}
{{- end -}}

{{/*
The object-store backend config block (instance.config.infrastructure.blob, ADR-058),
resolved safely to an empty dict when absent — e.g. under instance.existingSecret, where
the config is managed out-of-band and not visible to the chart. Returned as JSON for the
caller to fromJson. Shared by the blob PVC template and the deployment mount so both read
the exact same backend + directory.
*/}}
{{- define "devicechain.blobBackendConfig" -}}
{{- ((.Values.instance.config | default dict).infrastructure | default dict).blob | default dict | toJson -}}
{{- end -}}

{{/*
The filesystem object-store PVC name: blobStorage.persistence.existingClaim when supplied,
else the chart-created default. Shared by the PVC template and the deployment volume so a
rendered claim and its mount always agree.
*/}}
{{- define "devicechain.blobClaimName" -}}
{{- $p := (.Values.blobStorage | default dict).persistence | default dict -}}
{{- $p.existingClaim | default (printf "dci-%s-blob" .Values.instance.id) -}}
{{- end -}}

{{/*
The per-service config ConfigMap `data` block: one key per enabled area. Factored
out so the rendered ConfigMap and the E8 checksum annotation are computed from the
exact same source. Takes the root context.
*/}}
{{- define "devicechain.microserviceConfig" -}}
{{- $root := . -}}
{{- range $area := splitList "," (include "devicechain.enabledAreas" $root) }}
{{- $areaCfg := get ($root.Values.functionalAreas | default dict) $area | default dict }}
{{- $cfg := get $areaCfg "config" | default dict }}
{{- if eq $area "mcp" }}
{{- $cfg = include "devicechain.mcpMergedConfig" $root | fromJson }}
{{- include "devicechain.validateMcpConfig" (dict "root" $root "cfg" $cfg) }}
{{- end }}
{{ $area }}: {{ $cfg | toJson | quote }}
{{- end }}
{{- end -}}

{{/*
devicechain.publicOrigin is the external origin this instance is reachable on
(scheme + ingress host) — the base every externally-meaningful URL derives from.
Lowercased: an OAuth issuer must be lowercase or user-management's validateIssuerUrl
rejects it and the service CrashLoops. Empty when there is no ingress, since then
there is no external origin to speak of.
*/}}
{{- define "devicechain.publicOrigin" -}}
{{- if and .Values.ingress.enabled .Values.ingress.host -}}
{{- $scheme := "http" -}}
{{- if .Values.ingress.tls.enabled -}}{{- $scheme = "https" -}}{{- end -}}
{{- printf "%s://%s" $scheme (.Values.ingress.host | lower) -}}
{{- end -}}
{{- end -}}

{{/*
devicechain.mcpDerivedConfig supplies mcp's two REQUIRED urls (ADR-047) from the
ingress, as JSON, so the area comes up on a profile that ships it rather than
CrashLooping on config the operator had no way to know it owed. An explicit
functionalAreas.mcp.config value always wins over these.

The /api/<area> prefix is the ingress convention (see ingress.yaml), and issuerUrl
MUST equal user-management's auth.issuerUrl byte-for-byte — an RFC 8414 issuer is
compared exactly, not parsed — so both derive from the one origin above rather than
being spelled out twice. Renders {} with no ingress: there is no origin to derive
from, and mcp then fails startup closed on its own required-field check, which is
the honest outcome for an externally-facing OAuth resource server with no external
address.
*/}}
{{- define "devicechain.mcpDerivedConfig" -}}
{{- $origin := include "devicechain.publicOrigin" . -}}
{{- if $origin -}}
{{- dict "resourceUrl" (printf "%s/api/mcp" $origin) "issuerUrl" (printf "%s/api/user-management" $origin) | toJson -}}
{{- else -}}
{{- dict | toJson -}}
{{- end -}}
{{- end -}}

{{/*
devicechain.validateMcpConfig fails the render when mcp's required URLs are absent or
unusable, rather than letting the pod CrashLoop on its own config check where the
reason is a log line away. Takes a dict {root, cfg} of the FINAL merged config.

The http case is the sharp one: mcp accepts http only for a loopback host, so a
no-TLS ingress on a real hostname derives a URL it will reject at startup. dcctl
guards the same combination for Grafana SSO; this is the chart-side equivalent.

🔴 The loopback test reads the host out of THE URL BEING VALIDATED, not out of
ingress.host, and that is the whole point of the check. It used to read ingress.host,
which is only the same string while the URL was derived from the ingress. An operator
who set functionalAreas.mcp.config.resourceUrl explicitly — the documented escape hatch,
and the only way to point mcp at a host the chart does not own — had it validated
against a completely different host: http://localhost:8080 on an instance whose ingress
host is iot.example.com failed the render for "not a loopback host" while being exactly
what the service accepts (its own check parses the URL and compares its hostname). It
errs toward refusing, so nothing shipped broken, but the reason it gave was about a
host that had nothing to do with the value.

Port included or not makes no difference here: urlParse's `hostname` drops it, which is
also what the service's own url.Hostname() comparison does.
*/}}
{{- define "devicechain.validateMcpConfig" -}}
{{- $cfg := .cfg -}}
{{- range $field := list "resourceUrl" "issuerUrl" -}}
  {{- $v := get $cfg $field | default "" -}}
  {{- if not $v -}}
    {{- fail (printf "mcp: %s is required and could not be derived — the area is enabled (profile \"full\" ships it) but no ingress is configured to derive it from. Set ingress.enabled + ingress.host, or set functionalAreas.mcp.config.%s explicitly." $field $field) -}}
  {{- end -}}
  {{- $host := (urlParse $v).hostname | default "" | lower -}}
  {{- $loopback := or (eq $host "localhost") (eq $host "127.0.0.1") (eq $host "::1") -}}
  {{- if and (hasPrefix "http://" $v) (not $loopback) -}}
    {{- fail (printf "mcp: %s would be %q, whose host %q is not a loopback address — mcp rejects that at startup, because it allows http only for localhost, 127.0.0.1 or ::1. Set ingress.tls.enabled=true, use ingress.host=localhost, or set functionalAreas.mcp.config.%s to an https URL." $field $v $host $field) -}}
  {{- end -}}
  {{- if hasSuffix "/" $v -}}
    {{- fail (printf "mcp: %s is %q, which ends with a trailing slash. mcp refuses that at startup: the identifier is compared byte-for-byte as the token audience and as the `resource` field of its metadata document, so it must have exactly one spelling. Drop the trailing slash." $field $v) -}}
  {{- end -}}
{{- end -}}

{{/*
🔴 AN OVERRIDE THE INGRESS CANNOT ROUTE IS REFUSED, RATHER THAN RENDERED AND LEFT TO
FAIL AS A CLIENT PROBLEM. The identifier is not only a name: it is the URL a client
POSTs to, and the origin it derives the metadata location from. Under an ingress, the
only identifier this chart actually routes is the derived one — the /api/mcp rule is
keyed on the area name and every rule is keyed on ingress.host. An override pointing
anywhere else rendered cleanly and produced an instance whose tokens are bound to an
identifier no route delivers and whose metadata document sits on a different host. The
chart could see that and said nothing.

The loopback exception is the reason the override exists at all: a port-forwarded or
locally-run mcp is reached at http://localhost:<port>, which no ingress rule serves and
none needs to.
*/}}
{{- if .root.Values.ingress.enabled -}}
  {{- $v := get $cfg "resourceUrl" | default "" -}}
  {{- $host := (urlParse $v).hostname | default "" | lower -}}
  {{- $loopback := or (eq $host "localhost") (eq $host "127.0.0.1") (eq $host "::1") -}}
  {{- $derived := get (include "devicechain.mcpDerivedConfig" .root | fromJson) "resourceUrl" | default "" -}}
  {{- if and (not $loopback) (ne $v $derived) -}}
    {{- fail (printf "mcp: resourceUrl is %q, but this instance's ingress only routes %q. That identifier is the URL a client POSTs to as well as the name its token is bound to, so an unrouted one yields tokens for an address that answers nothing and a metadata document on the wrong host. Either leave it unset (it is derived from the ingress), set it to %q, or point it at a loopback address if you are reaching mcp by port-forward rather than through the ingress." $v $derived $derived) -}}
  {{- end -}}
{{- end -}}
{{- end -}}

{{/*
devicechain.mcpMergedConfig is the config mcp actually receives, as JSON: the explicit
functionalAreas.mcp.config merged over the ingress-derived defaults. Merged into a FRESH
dict, so the explicit config wins and .Values is never mutated.

🔴 It is ONE definition because two templates now depend on it and they must not
disagree. microserviceConfig hands resourceUrl to the service as its token audience and
as the `resource` field of its metadata document; ingress.yaml routes the path that
document is fetched at, which is derived from the same identifier. Two derivations would
let the routed path drift away from the identifier with nothing to notice — and a
metadata document reachable at a path that does not match its own `resource` field is
rejected by the client rather than used.
*/}}
{{- define "devicechain.mcpMergedConfig" -}}
{{- $explicit := get (get (.Values.functionalAreas | default dict) "mcp" | default dict) "config" | default dict -}}
{{- merge (dict) $explicit (include "devicechain.mcpDerivedConfig" . | fromJson) | toJson -}}
{{- end -}}

{{/*
devicechain.mcpResourceUrl is mcp's FINAL resource identifier. Empty when there is
neither an explicit value nor an ingress to derive one from, which is the state
validateMcpConfig fails the render on.
*/}}
{{- define "devicechain.mcpResourceUrl" -}}
{{- get (include "devicechain.mcpMergedConfig" . | fromJson) "resourceUrl" | default "" -}}
{{- end -}}

{{/*
devicechain.userManagementIssuerUrl is the OAuth 2.1 Authorization Server's issuer, or
empty when the AS is off.

It is operator-set (functionalAreas.user-management.config.auth.issuerUrl) and NOT
derived from the ingress, deliberately: setting an issuer changes the `iss` claim of
every token the instance mints, not only the ones MCP uses, so it is its own switch.
The ingress uses it to decide whether to route the AS metadata document — with no
issuer there is no AS, and a route to one would be a route to a 404.
*/}}
{{- define "devicechain.userManagementIssuerUrl" -}}
{{- $cfg := get (get (.Values.functionalAreas | default dict) "user-management" | default dict) "config" | default dict -}}
{{- get (get $cfg "auth" | default dict) "issuerUrl" | default "" -}}
{{- end -}}

{{/*
devicechain.validateSecretsRootKey fails the render when no instance root key is
configured. Every instance needs one, whatever its profile: user-management — a core
area, enabled in every profile — seals the JWT signing key's private half in its
ADR-059 envelope-encrypted secret store, and a service that cannot form its KEK MUST
NOT start ("encryption-at-rest is not optional once wired"). Without this check the
only symptom is a CrashLooping user-management and an instance nobody can sign in to.

It used to consult a list of the areas that own a secret store and fail only when one
of them was enabled, which let the telemetry and ingest-only profiles render without a
key. That list is gone rather than extended: an area that is always enabled makes the
question "does this install carry a secret-store area?" always answer yes.
*/}}
{{- define "devicechain.validateSecretsRootKey" -}}
{{- $rootKey := "" -}}
{{- with .Values.instance -}}{{- with .config -}}{{- with .infrastructure -}}{{- with .secrets -}}
{{- $rootKey = .rootKey | default "" -}}
{{- end -}}{{- end -}}{{- end -}}{{- end -}}
{{- if not $rootKey -}}
  {{- fail "instance.config.infrastructure.secrets.rootKey is required: every instance seals its token-signing key under it, along with any integration credentials it stores, and user-management cannot start without it. Set it to a base64 256-bit key (openssl rand -base64 32); dcctl bootstrap mints one automatically." -}}
{{- end -}}
{{- end -}}

{{/*
The hand-set shutdown refusal, split out of devicechain.instanceConfig so it runs
on BOTH config sources.

🔴 IT WAS INSIDE THE DOCUMENT RENDERER, AND THAT IS EXACTLY THE PLACE IT COULD
STOP RUNNING. Under instance.existingSecret the chart writes no document, so
every check living inside the renderer leaves with it — silently, because a
template that is not rendered raises nothing. Splitting the refusal out lets
devicechain.validateInstanceConfigSource invoke it unconditionally, and the
renderer still invokes it too, so the inline path is unchanged.
*/}}
{{- define "devicechain.validateShutdownNotHandSet" -}}
{{- $infra := ((.Values.instance.config | default dict).infrastructure | default dict) -}}
{{- if hasKey $infra "shutdown" -}}
  {{- fail "instance.config.infrastructure.shutdown is set by the chart, not by hand: it is written from the top-level shutdownDrainSeconds and terminationGracePeriodSeconds values so the drain window and the pod's grace period cannot disagree. Remove the block and set those two values instead." -}}
{{- end -}}
{{- end -}}

{{/*
🔴 THE GATE THAT SURVIVES BOTH CONFIG SOURCES. Invoked unconditionally from
instance-config.yaml — outside the block that renders the Secret, so it runs on
the path where no Secret is rendered at all.

The chart has two config sources and they are not symmetrical. With inline
config the chart is the AUTHOR: it can read the root key, refuse a hand-set
shutdown block, drop the ai-inference coordinates a disabled area must not see,
and hash the exact bytes it is about to write. Under instance.existingSecret the
chart is only the MOUNTER — the document is opaque to it — so every one of those
checks becomes something it cannot do.

Which is fine, and is not what this gate is about. The danger is that they
disappear with nothing said: one value switches five checks off, the templates
still render, the install still succeeds, and the tests that covered them keep
passing against an inline-config fixture no deployment uses any more. That is the
shape this project keeps finding, and it does not need a fourteenth instance.

So the gate makes the handover EXPLICIT. Whoever supplies the Secret must also
supply instance.existingSecretChecksum — a digest of the document they wrote. It
is not decoration:

  - It is what keeps pods rolling when the config changes. The checksum annotation
    hashes the rendered document, which under an external Secret is empty and
    therefore constant: a credential rotation would apply cleanly, roll nothing,
    and report success.
What it does NOT do, stated here because the first draft of this comment claimed
otherwise: it does not prove the supplier holds the document, and it does not
make the document valid. A digest is 64 hex characters; anyone can type 64 hex
characters. The requirement buys one specific thing — a rollout trigger the
supplier declares and is responsible for changing — and the checks the chart lost
are closed elsewhere, in the pre-flight that strict-loads the authored bytes
(backend/cli/bootstrap/instance_config.go). Reading more into it than that is how
a gate ends up trusted for something it never checked.
*/}}
{{- define "devicechain.validateInstanceConfigSource" -}}
{{- include "devicechain.validateShutdownNotHandSet" . -}}
{{- $external := .Values.instance.existingSecret | default "" -}}
{{- if $external -}}
  {{/* 🔴 THE NAME IS NOT FREE, AND LETTING IT BE FREE COSTS EVERY CREDENTIAL.
  dcctl reads this Secret back by the name the chart would have given it — see
  DeployedInstanceConfig — and a NotFound there does not mean "cannot tell", it
  means "fresh install, mint everything". So an external Secret under any other
  name does not fail: the next bootstrap re-run reads nothing, decides the
  instance is new, and rotates the root key, both database passwords and every
  broker credential out from under a live instance. This project has already had
  that incident once from a different cause. The chart offered the flexibility;
  nothing anywhere enforced the constraint it depends on. */}}
  {{- $want := printf "dci-%s-config" .Values.instance.id -}}
  {{- if ne $external $want -}}
    {{- fail (printf "instance.existingSecret must be named %q and is %q. dcctl reads the instance config back by that exact name to decide whether this instance already exists, and reads a missing Secret as a FRESH INSTALL — so a differently-named Secret would make the next bootstrap re-run mint a new root key and new database and broker credentials over a live instance. Rename the Secret, or supply the config inline." $want $external) -}}
  {{- end -}}
  {{- $sum := .Values.instance.existingSecretChecksum | default "" -}}
  {{- if not $sum -}}
    {{- fail (printf "instance.existingSecret is set to %q, so instance.existingSecretChecksum must be set too. The chart cannot read that Secret, so it cannot hash what the pods will actually mount: without a digest supplied by whoever wrote it, changing the instance config would apply cleanly, roll no pods, and report success. Set it to the sha256 of the `instance` document in that Secret. Supplying the config inline instead leaves the chart to compute it." $external) -}}
  {{- end -}}
  {{/* 🔴 SHAPE-CHECKED, NOT MERELY NON-EMPTY, and the difference is the whole
  guard. This value is interpolated into a pod annotation, so anything that is
  not a digest is one of two failures and both defeat the check: a whitespace or
  YAML-null string ("&nbsp;", "~", "null") renders as a NULL annotation — the
  constant this gate exists to prevent, reached through a gate that passed — and
  a value carrying a newline or a colon either breaks the manifest or injects
  further annotation keys. values.schema.json carries the same pattern, but a
  schema can be skipped (--skip-schema-validation) and this cannot. */}}
  {{- if not (regexMatch "^[0-9a-f]{64}$" $sum) -}}
    {{- fail (printf "instance.existingSecretChecksum must be a sha256 digest — 64 lowercase hex characters — and is %q. It becomes a pod annotation, so a value that is blank, YAML-null, or carries a colon or newline would either render as a constant (rolling no pods when the config changes, which is the failure this value exists to prevent) or corrupt the pod template." $sum) -}}
  {{- end -}}
  {{/* 🔴 TEMPLATES THAT READ instance.config CANNOT SEE THE MOUNTED DOCUMENT.
  values.schema.json makes instance.config REQUIRED, so under an external Secret
  that block is not absent — it is the chart's defaults, and every template
  reading it renders happily from coordinates the pods may not be using. Two do
  so in ways nothing downstream would notice: the NetworkPolicy takes its egress
  ports from the config, and the NATS PodMonitor takes its namespace from the
  broker hostname. A wrong port silently blocks outbound egress; a wrong
  namespace silently monitors nothing. Both are refused here rather than
  rendered from a guess, because a guess that happens to match the default is
  indistinguishable from a correct one until it does not. */}}
  {{/* Each takes the same escape hatch blobStorage.persistence.mountPath already
  uses: restate the coordinate as a top-level value, where the supplier of the
  document can set it to what the document actually says. Refusing outright was
  the first draft and was wrong — metrics.natsPodMonitor is ON by default, so it
  would have made the external path fail out of the box for a monitoring
  nicety. */}}
  {{- if and .Values.networkPolicy.enabled (not .Values.networkPolicy.externalConfigPorts) -}}
    {{- fail "networkPolicy.enabled with instance.existingSecret needs networkPolicy.externalConfigPorts set (keys `nats` and `rdb`). The egress ports are normally read from instance.config, which under an external Secret is the chart's DEFAULTS rather than the document the pods mount — and a port that does not match silently blocks the services' own egress, which looks like a broker or database outage. Restate them, or set networkPolicy.enabled=false." -}}
  {{- end -}}
  {{- if and .Values.metrics.enabled .Values.metrics.natsPodMonitor (not .Values.metrics.natsBrokerHost) -}}
    {{- fail "metrics.natsPodMonitor with instance.existingSecret needs metrics.natsBrokerHost set (the broker's hostname: the short Service name for a broker in this instance's namespace, or <service>.<namespace> for one elsewhere). The PodMonitor's target namespace is normally derived from instance.config.infrastructure.nats.hostname, which under an external Secret is the chart's DEFAULT rather than the broker the pods use — and a wrong namespace monitors nothing, silently. Restate it, or set metrics.natsPodMonitor=false." -}}
  {{- end -}}
{{- else -}}
  {{- include "devicechain.validateSecretsRootKey" . -}}
{{- end -}}
{{- end -}}

{{/*
The value of the checksum/instance-secret pod annotation: what must change when
the mounted instance config changes.

With inline config that is the digest of the document the chart is about to
write. Under instance.existingSecret the chart cannot see the document, so it is
the digest its supplier declared — required by
devicechain.validateInstanceConfigSource, for this reason.
*/}}
{{- define "devicechain.instanceConfigChecksum" -}}
{{- if .Values.instance.existingSecret | default "" -}}
{{- .Values.instance.existingSecretChecksum -}}
{{- else -}}
{{- include "devicechain.instanceConfig" . | sha256sum -}}
{{- end -}}
{{- end -}}

{{/* The dedicated ServiceAccount name (E7). */}}
{{- define "devicechain.serviceAccountName" -}}
{{- .Values.serviceAccount.name | default (printf "dc-%s" .Values.instance.id) -}}
{{- end -}}

{{/*
The node-loss eviction fuse, as a `tolerations:` block, or nothing when the value
is unset. Takes the ROOT context. Emits at the pod-spec indent (6 spaces).

🔴 A HELPER RATHER THAN AN INLINE COPY PER TEMPLATE, and the reason is not
tidiness. The first version of this WAS two inline copies, and it shipped as one:
deployment.yaml got the block, frontend.yaml did not, and the chart rendered ten
Deployments of which nine carried the fuse. Nothing failed — the tenth simply kept
Kubernetes' 300s default, which is invisible precisely because an unset toleration
is not an absent one. A cross-check of "Deployments rendered" against "tolerations
rendered" is what caught it. Anything that grows a new pod-spec template inherits
the fix from here instead of needing to remember it.

`if not (kindIs "invalid" ...)` rather than `with`: 0 is a legitimate value here —
"evict the moment the taint lands" — and `with` is falsy on 0, so the most
aggressive request in the values file would render nothing and silently deliver
the least aggressive behaviour. Only unset means "leave Kubernetes' default".
*/}}
{{- define "devicechain.nodeLossTolerations" -}}
{{- if not (kindIs "invalid" .Values.nodeLossTolerationSeconds) }}
{{- $tol := .Values.nodeLossTolerationSeconds | int }}
tolerations:
  - key: node.kubernetes.io/not-ready
    operator: Exists
    effect: NoExecute
    tolerationSeconds: {{ $tol }}
  - key: node.kubernetes.io/unreachable
    operator: Exists
    effect: NoExecute
    tolerationSeconds: {{ $tol }}
{{- end }}
{{- end -}}

{{/*
devicechain.podAntiAffinity: the pod's PREFERRED anti-affinity, composed from every rule
that wants one, because a pod spec has one affinity block and two helpers each rendering
`affinity:` would collide. Two terms, each rendered only when its rule applies, in this
order:

1. Against this instance's event-store primary, for an area whose values set
avoidEventStorePrimary.

The selector is the label CloudNativePG puts on a primary pod and moves on promotion
(cnpg.io/instanceRole: primary, the label dcctl's own database checks read). No
namespace is named, so the term matches only pods in this pod's own namespace: the
instance namespace, whose only CloudNativePG cluster is the event store (the
relational store runs in the cluster's infrastructure namespace). That is also why no
cluster name appears here: the namespace makes the distinction, and there is no name
to keep in step with the OpenTofu root. An install whose event store is not a
CloudNativePG cluster has no pod with that label, so the term matches nothing.

Preferred, never required: a required term would leave the pod Pending on a cluster
with fewer nodes than busy services. IgnoredDuringExecution: a failover that promotes
a standby on this pod's node does not move the pod; the next reschedule of either does.

Measured on GKE (values.yaml, device-management): with every area requesting the same
CPU, the scheduler put the event-store primary, event-management and replicas of
device-management and event-sources on one node.

2. Against the area's OWN pods, for an area with eventPathSpread above one replica. A
pod that sets a topology spread of its own loses the scheduler's DEFAULT spread (one
Deployment's replicas across nodes and zones), and the event-path spread alone lets one
area's replicas share a node: both event-management pods on one node and two other
event-path pods on each of the others has skew 0 and satisfies it. Two pods that share
a node share its CPU and are lost together, which is what running two was for. The
selector is the area's labels (the Deployment's selector, rendered by the same helper),
so a rollout's old pods count too, which is right while they still occupy their nodes.

This is NOT a second topology spread constraint, and must not become one. The API
server allows one constraint per (topologyKey, whenUnsatisfiable) pair whatever the
selectors, so a second hostname/ScheduleAnyway entry makes the Deployment invalid on
create; and a strategic-merge patch keys that list on topologyKey ALONE, so a Helm
upgrade merges two hostname entries into one constraint counting only this area's pods,
which silently drops the event-path spread. A second entry with DoNotSchedule is legal
on create but required, which would leave a rollout's surge pod Pending on a small
cluster, and it shares the merge key all the same. The zone half of the default spread
is not restored.

Both weight 100: neither preference was measured against the other. The list has no
patch strategy, so an upgrade replaces it whole: a change of replica count or of either
switch takes effect in one upgrade.
Parameters: areaCfg, replicas (the area's resolved count), areaLabels (its labels).
*/}}
{{- define "devicechain.podAntiAffinity" -}}
{{- $primary := get .areaCfg "avoidEventStorePrimary" -}}
{{- $own := and (include "devicechain.eventPathLabel" (dict "areaCfg" .areaCfg)) (gt (int .replicas) 1) -}}
{{- if or $primary $own -}}
affinity:
  podAntiAffinity:
    preferredDuringSchedulingIgnoredDuringExecution:
{{- if $primary }}
      - weight: 100
        podAffinityTerm:
          topologyKey: kubernetes.io/hostname
          labelSelector:
            matchLabels:
              cnpg.io/instanceRole: primary
{{- end }}
{{- if $own }}
      - weight: 100
        podAffinityTerm:
          topologyKey: kubernetes.io/hostname
          labelSelector:
            matchLabels:
              {{- .areaLabels | nindent 14 }}
{{- end }}
{{- end }}
{{- end -}}

{{/*
devicechain.eventPathLabel: the pod label the event-path spread counts, for an area
whose values set eventPathSpread. Rendered into the pod TEMPLATE's labels only, never
into the Deployment selector: a selector is immutable, so adding a key to it would make
every upgrade of an existing instance fail.
devicechain.eventPathSpread builds its selector by calling this helper, so the label a
pod carries and the label the constraint counts cannot be spelled two ways; and
devicechain.podAntiAffinity calls it as the gate for the term over the area's own pods,
which exists only where this spread does.
Parameters: areaCfg.
*/}}
{{- define "devicechain.eventPathLabel" -}}
{{- if get .areaCfg "eventPathSpread" -}}
devicechain.io/event-path: "true"
{{- end -}}
{{- end -}}

{{/*
devicechain.eventPathSpread: a PREFERRED topology spread over nodes among this
instance's event-path pods (every area whose values set eventPathSpread).

The pods belong to different Deployments, so the selector is a label they share rather
than any one Deployment's own labels. No instance label: a topology spread counts only
pods in the pod's own namespace, which is the instance namespace, so the namespace makes
the distinction (as for devicechain.podAntiAffinity above). No
matchLabelKeys: pod-template-hash there would narrow the count to the pod's own
Deployment revision, and the spread would separate nothing.

ScheduleAnyway, never DoNotSchedule: on a cluster with fewer nodes than these pods (kind,
one node, --compact) every pod still schedules. It is a score the scheduler weighs with
others (free CPU, the anti-affinity above), so on three nodes it usually, not always,
keeps them to two per node: what it guards against is three or more of the five on one
node. It does not explain or fix the placement measured on GKE at shipped defaults
(event-sources + event-processing, event-management alone, device-management +
device-state; two service nodes at about 80% CPU and the third at 55% at 6,000 events/s):
that 2/1/2 split already satisfies maxSkew 1. It came from requests far below use, and
the measured requests in values.yaml are what address it.

Which NATS server leads a stream is NATS's choice, so no rule here can keep a service
off the busiest broker's node. What this does is cap how many of these pods share one.

A pod that sets a spread of its own loses the scheduler's DEFAULT spread. Above one
replica the area's own pods get their node preference back from
devicechain.podAntiAffinity, not from a second constraint here: see that helper for why
a second hostname/ScheduleAnyway constraint is refused on create and merged on upgrade.
Parameters: areaCfg.
*/}}
{{- define "devicechain.eventPathSpread" -}}
{{- with include "devicechain.eventPathLabel" (dict "areaCfg" .areaCfg) -}}
topologySpreadConstraints:
  - maxSkew: 1
    topologyKey: kubernetes.io/hostname
    whenUnsatisfiable: ScheduleAnyway
    labelSelector:
      matchLabels:
        {{- . | nindent 8 }}
{{- end }}
{{- end -}}

{{/* Identifying labels for an instance-scoped resource (namespace, ConfigMaps). */}}
{{- define "devicechain.instanceLabels" -}}
devicechain.io/instance: {{ .Values.instance.id }}
{{- end -}}

{{/*
Identifying labels for a per-functional-area resource (Deployment/Service).
Takes a dict {root, area}. These are stable (instance + area only), so the same
set is safe for both metadata labels and selector matchLabels.
*/}}
{{- define "devicechain.areaLabels" -}}
devicechain.io/instance: {{ .root.Values.instance.id }}
devicechain.io/functional-area: {{ .area }}
{{- end -}}

{{/*
devicechain.memoryQuantityMiB converts a Kubernetes memory quantity to a whole
number of MiB (rounded down). Parameters: q (the quantity), area (for messages).

The conversion has to happen BEFORE any percentage is applied, and that ordering
is the entire reason this helper exists. Helm arithmetic is integer, so taking a
percentage of a MAGNITUDE and reattaching the unit turns a "1Gi" limit into
floor(1 * 0.75) = "0Gi" — the identical trap that made sizing the JetStream PV as
(sum / 0.9) unsafe, where flooring 90% of the magnitude silently produced a
ceiling below the sum it was meant to cover. Normalising to MiB first means 1Gi
becomes 1024, and 75% of it is 768.

It does not parse the quantity itself: devicechain.quantityScalar does, for this
and for the request/limit comparison, so the chart has one reading of a memory
quantity rather than two that disagree. What it adds is a POLICY: only the binary
suffixes are accepted. A limit written as 1G or 500M is a valid Kubernetes
quantity, but a GOMEMLIMIT derived from it is off by the factor between 1G and 1Gi
from what an operator who wrote "G" most likely meant, and that error would not
fail, only mislead. So a decimal or unitless limit is refused here.
*/}}
{{- define "devicechain.memoryQuantityMiB" -}}
{{- $q := .q | toString -}}
{{- if not (regexMatch "(Ki|Mi|Gi)$" $q) -}}
  {{- fail (printf "memory quantity %q must use a binary suffix (Ki/Mi/Gi) so GOMEMLIMIT can be derived from it; a decimal or unitless limit is refused rather than read as the binary size it most likely meant" $q) -}}
{{- end -}}
{{- $bytes := include "devicechain.quantityScalar" (dict "dim" "memory" "q" $q "area" .area) | float64 -}}
{{- divf $bytes 1048576.0 | floor | int64 -}}
{{- end -}}

{{/*
devicechain.areaResources renders one area's container resources from three
layers, each merged over the one before it KEY BY KEY:

  1. the top-level `resources` map;
  2. the area's `functionalAreas.<area>.measuredRequests` (CPU requests: each
     area's use at 6,000 events/s, values.yaml), over the requests only, and only while
     `useMeasuredRequests` is true;
  3. the area's own `functionalAreas.<area>.resources`, which wins over both.

The measured requests are a layer of their own, with a switch, rather than values
under the area's `resources`, because `dcctl install --compact` lowers requests by
writing ONE top-level map and must reach every area. Were they under `resources`,
they would win over compact's top-level requests, and the only repair would be for
dcctl to write per-area requests: a second list, in dcctl, of which areas the chart
sizes. Compact sets `useMeasuredRequests: false` instead, and dcctl stays ignorant
of the list.

It is a merge and not a replacement because the top-level map is written by more
than one hand. `dcctl install --compact` lowers the top-level REQUESTS; the chart
raises some areas' CPU LIMITS (the event-path areas, values.yaml).
Under replacement, an area with a block of its own lost whichever half it did not
restate: raising one limit rendered that area with no requests at all, which took
it out of the compact preset, and the only repair was for dcctl to know which areas
the chart overrides.

Both call sites read this one helper (the pod's resources in deployment.yaml and
the memory limit GOMEMLIMIT is derived from, below). A second expression for "this
area's resources" is how the two would come to describe different pods. The
console (frontend.yaml) is not a functional area and keeps its own block.

deepCopy on BOTH sides is load-bearing: mergeOverwrite writes into its first
argument, and without the copy the first area with a block of its own would be
merged INTO .Values.resources, handing its limits (or its measured request) to every
area rendered after it. The measured layer needs no copy of its own: it merges into
$base's requests, which the copy of .Values.resources already owns, and is only
ever read.

An area cannot REMOVE a key the top-level map sets (Helm deletes a null before any
template sees it, so it arrives here as "not set" and the default fills it). To
run an area without a limit, remove it from the top-level map and set it on the
areas that want one.

A request above its limit is refused here, naming the area and which map each of
the two came from: a top-level request can be refused against an area's own limit
that the operator never wrote (the chart sets the event-path areas' CPU limits),
and an operator's own lowered limit can be refused against a measured request they
never wrote either; a message naming only the area would send them looking for a
key that is not in their values. The API server would
refuse the pod anyway, but only when the ReplicaSet creates it, where `helm
upgrade` merely times out; and the merge makes it reachable from a values file
that used to be valid (an area that set only requests.memory above the top-level
limit used to get no limits at all).

Parameters: area (the name, for messages), areaCfg, root.
*/}}
{{- define "devicechain.areaResources" -}}
{{- $base := deepCopy (.root.Values.resources | default dict) -}}
{{- $measured := dict -}}
{{- if .root.Values.useMeasuredRequests -}}
{{- $measured = get .areaCfg "measuredRequests" | default dict -}}
{{- end -}}
{{- if $measured -}}
{{- $_ := set $base "requests" (mergeOverwrite (get $base "requests" | default dict) $measured) -}}
{{- end -}}
{{- $own := deepCopy (get .areaCfg "resources" | default dict) -}}
{{- $res := mergeOverwrite $base $own -}}
{{- range $dim := list "cpu" "memory" -}}
{{- $req := dig "requests" $dim "" $res | toString -}}
{{- $lim := dig "limits" $dim "" $res | toString -}}
{{- if and $req $lim -}}
{{- $r := include "devicechain.quantityScalar" (dict "dim" $dim "q" $req "area" $.area) | float64 -}}
{{- $l := include "devicechain.quantityScalar" (dict "dim" $dim "q" $lim "area" $.area) | float64 -}}
{{- if gt $r $l -}}
{{- $area := printf "functionalAreas.%s.resources" $.area -}}
{{- $reqAt := printf "the top-level resources.requests.%s" $dim -}}
{{- if hasKey $measured $dim -}}
{{- $reqAt = printf "functionalAreas.%s.measuredRequests.%s, the chart's measured default for this service (set useMeasuredRequests: false to turn the measured requests off, or set %s.requests.%s)" $.area $dim $area $dim -}}
{{- end -}}
{{- if hasKey (dig "requests" dict $own) $dim -}}
{{- $reqAt = printf "%s.requests.%s" $area $dim -}}
{{- end -}}
{{- $limAt := printf "the top-level resources.limits.%s" $dim -}}
{{- $fix := "Raise the limit or lower the request." -}}
{{- if hasKey (dig "limits" dict $own) $dim -}}
{{- $limAt = printf "%s.limits.%s, which is set by your values or by the chart's own default for this service" $area $dim -}}
{{- $fix = printf "A service's own limit is not replaced by a top-level one, so raise %s.limits.%s as well, or lower the request." $area $dim -}}
{{- end -}}
  {{- fail (printf "%s: the %s request %s is above the %s limit, and Kubernetes refuses such a pod. The request comes from %s and the limit from %s. %s" $area $dim $req $lim $reqAt $limAt $fix) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- toYaml $res -}}
{{- end -}}

{{/*
devicechain.quantityScalar converts a Kubernetes quantity to a comparable number:
millicores for dim "cpu", bytes for dim "memory". It is the chart's one reader of a
quantity: devicechain.areaResources uses it to compare a request with its limit
across units (1Gi against 256Mi, 1 against 750m), and devicechain.memoryQuantityMiB
to derive GOMEMLIMIT.

A form it does not recognise is an error, not a zero: a zero would make every
request look below its limit, which is the one answer the comparison exists to
withhold. It reads the forms a values file uses (a decimal number with an optional
exponent, then "m" for cpu or a binary or decimal suffix for memory), not every
form Kubernetes accepts; the rest (memory in millibytes, cpu with a decimal
suffix) are refused rather than guessed.

Parameters: dim, q, area (for messages).
*/}}
{{- define "devicechain.quantityScalar" -}}
{{- $q := .q | toString -}}
{{- $number := "^([0-9]+(\\.[0-9]*)?|\\.[0-9]+)([eE][+-]?[0-9]+)?" -}}
{{- if eq .dim "cpu" -}}
{{- if not (regexMatch (printf "%sm?$" $number) $q) -}}
  {{- fail (printf "functionalAreas.%s.resources: cpu quantity %q is not a form the chart reads: write cores (\"2\", \"1.5\") or millicores (\"500m\")" .area $q) -}}
{{- end -}}
{{- if hasSuffix "m" $q -}}
{{- trimSuffix "m" $q | float64 -}}
{{- else -}}
{{- mulf (float64 $q) 1000 -}}
{{- end -}}
{{- else -}}
{{- if not (regexMatch (printf "%s(Ki|Mi|Gi|Ti|Pi|Ei|k|M|G|T|P|E)?$" $number) $q) -}}
  {{- fail (printf "functionalAreas.%s.resources: memory quantity %q is not a form the chart reads: write a number of bytes with an optional unit (Ki, Mi, Gi, Ti, k, M, G, T)" .area $q) -}}
{{- end -}}
{{- $num := regexFind $number $q -}}
{{- $mag := float64 $num -}}
{{- $unit := trimPrefix $num $q -}}
{{- $scale := get (dict "" 1.0 "k" 1e3 "M" 1e6 "G" 1e9 "T" 1e12 "P" 1e15 "E" 1e18 "Ki" 1024.0 "Mi" 1048576.0 "Gi" 1073741824.0 "Ti" 1099511627776.0 "Pi" 1125899906842624.0 "Ei" 1152921504606846976.0) $unit -}}
{{- mulf $mag $scale -}}
{{- end -}}
{{- end -}}

{{/*
devicechain.goMemLimit resolves the GOMEMLIMIT for one functional area, or "" to
leave it unset.

Go does not read the container's memory limit. Measured on Go 1.26: a process in a
container limited to 128m reports GOMEMLIMIT as math.MaxInt64 — no soft limit at
all — while GOMAXPROCS IS derived from the cgroup CPU limit. That asymmetry is why
this helper exists and why there is deliberately no GOMAXPROCS counterpart:
setting one would override the runtime's own container awareness with a worse
guess, while memory is genuinely unmanaged.

Note what this does NOT do. It does not shrink a service's footprint: measurement
across four workload shapes in a limited container found no reduction in heap_sys
and a GC CPU cost, because steady-state memory is governed by the live set and a
soft limit cannot go below it. What it offers is a CEILING — during a spike in live
heap the collector works harder instead of the heap doubling past the cgroup limit
and the pod being OOMKilled. Death versus degradation. That is why the default is
off (goMemLimitPercent: 0) and why this must never be quoted as part of a published
footprint number.

It is DERIVED from the area's own memory limit rather than configured
independently, because the two must move together. A hardcoded value keeps
throttling a service whose limit an operator later raises, and the symptom is GC
thrash with no visible cause: nothing connects the latency to a number set once
in a values file. Deriving it means there is one number.

The percentage leaves room for what the limit covers but GOMEMLIMIT does not —
goroutine stacks, mmap'd regions, and the runtime's own bookkeeping all count
against the cgroup while sitting outside the Go heap the limit governs. Aiming
GOMEMLIMIT at 100% of the container limit would OOMKill rather than collect.

Resolution order: an explicit per-area goMemLimit, then an explicit global one,
then the derivation, whose percentage is the area's own goMemLimitPercent when it
sets one and the top-level one otherwise. Setting the percentage to 0 (on an area,
or at the top level for the areas that set none) restores Go's default behaviour,
which is the escape hatch for a service that turns out to want an unbounded heap
more than a small one. The five event-path areas ship 75; every other area ships
nothing and so follows the top level, which is 0.
*/}}
{{- define "devicechain.goMemLimit" -}}
{{- $root := .root -}}
{{- $areaCfg := .areaCfg -}}
{{- $explicit := get $areaCfg "goMemLimit" | default $root.Values.goMemLimit -}}
{{- if $explicit -}}
{{- $explicit -}}
{{- else -}}
{{- /* An area's own percentage wins, a 0 included: that is how an event-path area (which
ships 75) turns it off. Otherwise the top-level one applies. */ -}}
{{- $pct := $root.Values.goMemLimitPercent | default 0 | int -}}
{{- if hasKey $areaCfg "goMemLimitPercent" -}}
{{- $pct = get $areaCfg "goMemLimitPercent" | default 0 | int -}}
{{- end -}}
{{- $res := include "devicechain.areaResources" (dict "area" .area "areaCfg" $areaCfg "root" $root) | fromYaml -}}
{{- $limit := dig "limits" "memory" "" $res -}}
{{- if and (gt $pct 0) $limit -}}
{{- $mib := include "devicechain.memoryQuantityMiB" (dict "q" $limit "area" .area) | int64 -}}
{{- $derived := div (mul $mib $pct) 100 -}}
{{- if lt $derived 1 -}}
  {{- fail (printf "GOMEMLIMIT derived from a %s memory limit at %d%% rounds to zero; raise the limit or set goMemLimit explicitly" $limit $pct) -}}
{{- end -}}
{{- printf "%dMiB" $derived -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
devicechain.goGc resolves GOGC for one functional area, or "" to leave Go's default (100).
The area's own gogc wins, a 0 included (which turns the setting off for that area);
otherwise the top-level gogc applies. The five event-path areas ship 400, measured with the
GOMEMLIMIT that devicechain.goMemLimit derives, which is what keeps a larger heap target
from passing the container limit: do not raise one without the other.
*/}}
{{- define "devicechain.goGc" -}}
{{- $gc := .root.Values.gogc | default 0 | int -}}
{{- if hasKey .areaCfg "gogc" -}}
{{- $gc = get .areaCfg "gogc" | default 0 | int -}}
{{- end -}}
{{- if gt $gc 0 -}}
{{- $gc -}}
{{- end -}}
{{- end -}}

{{/*
devicechain.profilerAddress is the address one functional area's opt-in profiling
listener binds, or "" when that area's listener is off (the default). deployment.yaml
writes a non-empty answer into DC_PROFILER_ADDRESS, which is the service's only
switch for it.

It is a per-area POD setting rather than a key in the instance configuration, and
that is deliberate. The instance document is one Secret shared by every area, so a
key there restarts every service when it changes — which disturbs exactly the steady
state a profile is taken to measure — and under instance.existingSecret (every dcctl
install) the chart cannot write it at all. An environment variable on one Deployment
restarts that service alone, and works whichever way the document is supplied. The
service still fails closed on it: a value it cannot use refuses startup.

The default is the pod's loopback address, reachable only through kubectl
port-forward. The listener is never a container port or a Service port; nothing here
renders one.

A TCP port the area's own pod already serves is refused HERE, because the service
would otherwise bind the profiler first and then fail its own listener with an error
that names the wrong one. The service refuses its own HTTP port too; this also covers
the area's TCP extraPorts, which only the chart knows. A UDP extraPort (lwm2m-ingest's
CoAPS) is a different port space and does not collide with the TCP profiler, so it is
not refused. The address's syntax is judged by the service, which is the one reader
that has to be right about it.
*/}}
{{- define "devicechain.profilerAddress" -}}
{{- $root := .root -}}
{{- $p := get .areaCfg "profiler" | default dict -}}
{{- if $p.enabled -}}
{{- $addr := $p.address | default "127.0.0.1:6060" -}}
{{- $port := regexFind "[0-9]+$" $addr -}}
{{- $taken := list (toString $root.Values.service.port) -}}
{{- range $x := get .areaCfg "extraPorts" | default list -}}
{{- if eq (upper ($x.protocol | default "TCP")) "TCP" -}}
{{- $taken = append $taken (toString $x.port) -}}
{{- end -}}
{{- end -}}
{{- if and $port (has $port $taken) -}}
{{- fail (printf "functionalAreas.%s.profiler.address %q uses port %s, which the %s pod already serves over TCP (its TCP ports: %s). Choose another port for the profiling listener." .area $addr $port .area (join ", " $taken)) -}}
{{- end -}}
{{- $addr -}}
{{- end -}}
{{- end -}}

{{/*
devicechain.validateProfilerAreas refuses a profiling listener turned on for an area
this release does not deploy. Such a setting renders nothing, so without this it
would be accepted and do nothing, and the first sign would be a refused port-forward.
*/}}
{{- define "devicechain.validateProfilerAreas" -}}
{{- $enabled := splitList "," (include "devicechain.enabledAreas" .) -}}
{{- range $area, $cfg := .Values.functionalAreas | default dict -}}
{{- $p := get ($cfg | default dict) "profiler" | default dict -}}
{{- if and $p.enabled (not (has $area $enabled)) -}}
{{- fail (printf "functionalAreas.%s.profiler.enabled is true, but %s is not deployed by this release, so it would turn nothing on. Deploy the area, or remove the setting." $area $area) -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
devicechain.instanceConfig renders the instance-wide configuration document, with
coordinates for areas this deployment did not enable removed.

It exists as a helper rather than inline in instance-config.yaml because TWO
templates must see the same bytes: the Secret that carries the config, and the
`checksum/instance-secret` pod annotation that rolls pods when it changes
(templates/deployment.yaml). Computing the filter in only one of them makes the
annotation describe a document nobody is served — an operator who enables
ai-inference would get the new Secret with no pod roll, so the coordinate would
sit unread until some unrelated change restarted the pod, and the feature would
stay dead. Same reasoning as devicechain.microserviceConfig on the next line of
that annotation block.

The annotation now reads devicechain.instanceConfigChecksum rather than this
helper directly, and only because a second config source exists: under
instance.existingSecret this renders a document nothing mounts, so hashing it
would produce a value that never moves. The two paths are the same requirement —
the annotation must change when the mounted config does — reached differently.

WHY ANYTHING IS FILTERED. values.yaml ships
infrastructure.aiInference.hostname non-empty by default and the whole
instance.config rides through verbatim, so a profile that never deploys
ai-inference still handed every service a hostname for it. The effect was not a
missing feature but a MISLEADING one: event-processing saw a non-empty hostname,
built its natural-language rule drafter, failed at DNS, and told the user "the
inference provider is unavailable, or this tenant has not enabled external AI
routing" — blaming tenant consent for a service the operator never deployed. The
honest message ("not enabled on this deployment") fires only when the drafter is
nil, so it could never appear on the profile that needed it. Unsetting the key
restores that path.

WHY THE SHUTDOWN BUDGET IS INJECTED RATHER THAN WRITTEN. The graceful-shutdown
window and the pod's grace period are ONE budget checked against itself: a service
refuses to start if its drain window does not leave room, inside the grace period,
for the teardown that closes in-flight connections, the broker consumers and the
database pool. So the two numbers cannot be allowed to come from two places. The
operator writes shutdownDrainSeconds and terminationGracePeriodSeconds once, at the
top level; the pod spec reads the second of them and this helper writes both into
the document the services validate, so the config and the pod are the same budget
by construction. Setting the block by hand under instance.config is refused rather
than silently overwritten.

deepCopy keeps .Values untouched, so nothing else that reads instance.config sees
the filtered document by accident.
*/}}
{{- define "devicechain.instanceConfig" -}}
{{- $cfg := deepCopy .Values.instance.config -}}
{{- if not (has "ai-inference" (splitList "," (include "devicechain.enabledAreas" .))) -}}
  {{- if hasKey $cfg "infrastructure" -}}
    {{- $_ := unset (index $cfg "infrastructure") "aiInference" -}}
  {{- end -}}
{{- end -}}
{{- if not (hasKey $cfg "infrastructure") -}}
  {{- $_ := set $cfg "infrastructure" dict -}}
{{- end -}}
{{- $infra := index $cfg "infrastructure" -}}
{{- include "devicechain.validateShutdownNotHandSet" . -}}
{{- $_ := set $infra "shutdown" (dict
    "drainSeconds" (int .Values.shutdownDrainSeconds)
    "terminationGracePeriodSeconds" (int .Values.terminationGracePeriodSeconds)) -}}
{{- $cfg | toJson -}}
{{- end }}
