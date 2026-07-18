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
{{- define "devicechain.enabledAreas" -}}
  {{- $standard := list "user-management" "device-management" "event-sources" "event-management" "device-state" "dashboard-management" "command-delivery" "notification-management" "event-processing" -}}
  {{- $profiles := dict
      "default"     $standard
      "full"        (concat $standard (list "ai-inference" "outbound-connectors" "mcp"))
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
  -}}
  {{- $known := list "user-management" "device-management" "event-sources" "event-management" "device-state" "dashboard-management" "command-delivery" "notification-management" "event-processing" "outbound-connectors" "mcp" "ai-inference" -}}

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
{{- /* merge into a FRESH dict: the explicit config wins, and .Values is never mutated. */ -}}
{{- $cfg = merge (dict) $cfg (include "devicechain.mcpDerivedConfig" $root | fromJson) }}
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
*/}}
{{- define "devicechain.validateMcpConfig" -}}
{{- $cfg := .cfg -}}
{{- $host := .root.Values.ingress.host | default "" | lower -}}
{{- $loopback := or (eq $host "localhost") (eq $host "127.0.0.1") (eq $host "::1") -}}
{{- range $field := list "resourceUrl" "issuerUrl" -}}
  {{- $v := get $cfg $field | default "" -}}
  {{- if not $v -}}
    {{- fail (printf "mcp: %s is required and could not be derived — the area is enabled (profile \"full\" ships it) but no ingress is configured to derive it from. Set ingress.enabled + ingress.host, or set functionalAreas.mcp.config.%s explicitly." $field $field) -}}
  {{- end -}}
  {{- if and (hasPrefix "http://" $v) (not $loopback) -}}
    {{- fail (printf "mcp: %s would be %q, which mcp rejects at startup — it allows http only for a loopback host. Set ingress.tls.enabled=true, use ingress.host=localhost, or set functionalAreas.mcp.config.%s to an https URL." $field $v $field) -}}
  {{- end -}}
{{- end -}}
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
      {{- fail (printf "instance.config.infrastructure.secrets.rootKey is required: area %q owns an envelope-encrypted secret store (ADR-059) and cannot form its KEK without it, so it would crash-loop. Set it to a base64 256-bit key (openssl rand -base64 32); dcctl bootstrap mints one automatically." $a) -}}
    {{- end -}}
  {{- end -}}
{{- end -}}
{{- end -}}

{{/* The dedicated ServiceAccount name (E7). */}}
{{- define "devicechain.serviceAccountName" -}}
{{- .Values.serviceAccount.name | default (printf "dc-%s" .Values.instance.id) -}}
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
