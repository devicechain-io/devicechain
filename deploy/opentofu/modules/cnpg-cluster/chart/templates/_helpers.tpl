{{- /*
Copyright The DeviceChain Authors
SPDX-License-Identifier: Apache-2.0
*/ -}}

{{- /*
cnpg-cluster.requireSixFields refuses a CloudNativePG schedule that does not have
six fields. Called with (dict "name" <cluster> "field" <values path> "value" <schedule>).

🔴 SIX FIELDS, NOT FIVE. CloudNativePG's schedule is NOT the Kubernetes CronJob
format: it carries a leading SECONDS field. This is the single easiest mistake to
make here and its failure mode is the quiet one -- a five-field entry is a
malformed schedule, so the operator never computes a next run. `tofu apply`
succeeds, the ScheduledBackup object exists, `kubectl get scheduledbackup` shows
it, and no backup is ever taken. The error lives in the object's status, which is
not a thing anyone reads on a cluster that looks healthy.

Worse, a five-field entry is not obviously wrong to a reader: "0 3 * * *" means
3am to every operator alive. Read as six fields it would mean something else
entirely. So this refuses at render time, where the message can say which format
it wanted.

One definition for every schedule scheduledbackup.yaml renders -- backup.schedule,
and backup.objectStoreSchedule when base backups are volume snapshots -- so the two
cannot be counted differently.
*/ -}}
{{- define "cnpg-cluster.requireSixFields" -}}
{{- $count := 0 -}}
{{- range (splitList " " (trim .value)) }}{{- if . }}{{- $count = add1 $count }}{{- end }}{{- end }}
{{- if ne $count 6 }}
  {{- fail (printf "cnpg-cluster %q: %s is %q, which has %d fields. CloudNativePG schedules take SIX -- the leading field is SECONDS, unlike a Kubernetes CronJob. A five-field schedule is not rejected by the API: the object is created, no next run is ever computed, and no backup is ever taken. Daily at 03:00 is \"0 0 3 * * *\"." .name .field .value $count) -}}
{{- end }}
{{- end -}}
