{{/*
Copyright The DeviceChain Authors
SPDX-License-Identifier: Apache-2.0
*/}}

{{/*
devicechain.enabledAreas resolves the deployment selection (a named profile or an
explicit set, defaulting to the full profile) and validates it against the
ADR-022 decision-2 dependency rules, then returns the enabled functional areas as
a comma-joined string. It FAILS the render on an invalid selection.

The catalog below mirrors backend/k8s/functionalarea (the Go source of truth that
the operator uses); keep the two in sync. Soft dependencies are intentionally not
encoded — pub/sub (ADR-003) makes an absent peer safe, so only hard edges gate.
*/}}
{{- define "devicechain.enabledAreas" -}}
  {{- $profiles := dict
      "full"        (list "user-management" "device-management" "event-sources" "event-management" "device-state" "command-delivery")
      "telemetry"   (list "user-management" "device-management" "event-sources" "event-management" "device-state")
      "ingest-only" (list "user-management" "device-management" "event-sources")
  -}}
  {{- $core := list "user-management" "device-management" -}}
  {{- $hard := dict
      "event-sources"    (list "device-management")
      "event-management" (list "device-management")
      "device-state"     (list "device-management")
      "command-delivery" (list "device-management")
  -}}
  {{- $known := list "user-management" "device-management" "event-sources" "event-management" "device-state" "command-delivery" -}}

  {{- $profile := .Values.profile | default "" -}}
  {{- $explicit := .Values.enabledFunctionalAreas | default (list) -}}
  {{- $enabled := list -}}
  {{- if and (ne $profile "") (gt (len $explicit) 0) -}}
    {{- fail "devicechain: set either profile or enabledFunctionalAreas, not both" -}}
  {{- else if ne $profile "" -}}
    {{- $enabled = index $profiles $profile -}}
    {{- if not $enabled -}}
      {{- fail (printf "devicechain: unknown profile %q (known: full, telemetry, ingest-only)" $profile) -}}
    {{- end -}}
  {{- else if gt (len $explicit) 0 -}}
    {{- $enabled = $explicit -}}
  {{- else -}}
    {{- $enabled = index $profiles "full" -}}
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
    {{- printf "%s/%s:%s" $root.Values.image.registry $area $root.Values.image.tag -}}
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
  {{- $tag := $img.tag | default .Values.image.tag -}}
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
The per-service config ConfigMap `data` block: one key per enabled area. Factored
out so the rendered ConfigMap and the E8 checksum annotation are computed from the
exact same source. Takes the root context.
*/}}
{{- define "devicechain.microserviceConfig" -}}
{{- $root := . -}}
{{- range $area := splitList "," (include "devicechain.enabledAreas" $root) }}
{{- $areaCfg := get ($root.Values.functionalAreas | default dict) $area | default dict }}
{{ $area }}: {{ get $areaCfg "config" | default dict | toJson | quote }}
{{- end }}
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
