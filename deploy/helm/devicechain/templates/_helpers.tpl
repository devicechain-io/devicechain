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
      "full"        (concat $standard (list "ai-inference" "outbound-connectors" "mcp" "sparkplug-ingest" "lwm2m-ingest"))
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
  -}}
  {{- $known := list "user-management" "device-management" "event-sources" "event-management" "device-state" "dashboard-management" "command-delivery" "notification-management" "event-processing" "outbound-connectors" "mcp" "ai-inference" "sparkplug-ingest" "lwm2m-ingest" -}}

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
The instance config Secret name. C2 (ADR-022 review): the instance config holds
persistence credentials, so it is rendered into a Secret (not a ConfigMap). When
instance.existingSecret is set, that name is used instead so an operator can point
at an External-Secrets-managed / pre-created Secret holding the `instance` key.
*/}}
{{- define "devicechain.instanceConfigSecret" -}}
{{- .Values.instance.existingSecret | default (printf "dci-%s-config" .Values.instance.id) -}}
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
devicechain.validateSecretsRootKey fails the render when an enabled area owns an
ADR-059 envelope-encrypted secret store but no instance root key is configured. Such a
service cannot form its KEK and MUST NOT start ("encryption-at-rest is not optional
once wired"), so without this the only symptom is a CrashLooping pod.

notification-management is in the DEFAULT profile, so this is not only a "full"
concern — any install owes a root key.
*/}}
{{- define "devicechain.validateSecretsRootKey" -}}
{{- $needsKey := list "notification-management" "outbound-connectors" "ai-inference" -}}
{{- $rootKey := "" -}}
{{- with .Values.instance -}}{{- with .config -}}{{- with .infrastructure -}}{{- with .secrets -}}
{{- $rootKey = .rootKey | default "" -}}
{{- end -}}{{- end -}}{{- end -}}{{- end -}}
{{- if not $rootKey -}}
  {{- range $a := splitList "," (include "devicechain.enabledAreas" .) -}}
    {{- if has $a $needsKey -}}
      {{- fail (printf "instance.config.infrastructure.secrets.rootKey is required: area %q owns an envelope-encrypted secret store and cannot form its KEK without it, so it would crash-loop. Set it to a base64 256-bit key (openssl rand -base64 32); dcctl bootstrap mints one automatically." $a) -}}
    {{- end -}}
  {{- end -}}
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
number of MiB.

The conversion has to happen BEFORE any percentage is applied, and that ordering
is the entire reason this helper exists. Helm arithmetic is integer, so taking a
percentage of a MAGNITUDE and reattaching the unit turns a "1Gi" limit into
floor(1 * 0.75) = "0Gi" — the identical trap that made sizing the JetStream PV as
(sum / 0.9) unsafe, where flooring 90% of the magnitude silently produced a
ceiling below the sum it was meant to cover. Normalising to MiB first means 1Gi
becomes 1024, and 75% of it is 768.

Only the binary suffixes are accepted. A decimal quantity (1G, 500M) or a bare
byte count would each need a different conversion, and guessing wrong here does
not fail — it silently sets a memory limit off by a factor of 1.07 or 1048576. So
an unrecognised unit is an error rather than a default.
*/}}
{{- define "devicechain.memoryQuantityMiB" -}}
{{- $q := . | toString -}}
{{- $mag := regexFind "^[0-9]+" $q -}}
{{- $unit := regexFind "[A-Za-z]*$" $q -}}
{{- if not $mag -}}
  {{- fail (printf "memory quantity %q has no numeric magnitude" $q) -}}
{{- end -}}
{{- if eq $unit "Mi" -}}
{{- $mag -}}
{{- else if eq $unit "Gi" -}}
{{- mul (int64 $mag) 1024 -}}
{{- else if eq $unit "Ki" -}}
{{- div (int64 $mag) 1024 -}}
{{- else -}}
  {{- fail (printf "memory quantity %q must use a binary suffix (Ki/Mi/Gi) so GOMEMLIMIT can be derived from it; a decimal or unitless quantity would be converted wrongly and silently" $q) -}}
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
then the derivation. Setting goMemLimitPercent to 0 disables it everywhere and
restores Go's default behaviour, which is the escape hatch for a service that
turns out to want an unbounded heap more than a small one.
*/}}
{{- define "devicechain.goMemLimit" -}}
{{- $root := .root -}}
{{- $areaCfg := .areaCfg -}}
{{- $explicit := get $areaCfg "goMemLimit" | default $root.Values.goMemLimit -}}
{{- if $explicit -}}
{{- $explicit -}}
{{- else -}}
{{- $pct := $root.Values.goMemLimitPercent | default 0 | int -}}
{{- $res := get $areaCfg "resources" | default $root.Values.resources -}}
{{- $limit := dig "limits" "memory" "" $res -}}
{{- if and (gt $pct 0) $limit -}}
{{- $mib := include "devicechain.memoryQuantityMiB" $limit | int64 -}}
{{- $derived := div (mul $mib $pct) 100 -}}
{{- if lt $derived 1 -}}
  {{- fail (printf "GOMEMLIMIT derived from a %s memory limit at %d%% rounds to zero; raise the limit or set goMemLimit explicitly" $limit $pct) -}}
{{- end -}}
{{- printf "%dMiB" $derived -}}
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
{{- $cfg | toJson -}}
{{- end }}
