{/* Copyright The DeviceChain Authors
    SPDX-License-Identifier: Apache-2.0 */}

# Troubleshooting and routine operations {#troubleshooting}

Start with the symptom below. If you already have a firing alert, use the
[alert runbook](./alert-runbook.md) for its meaning and response. The
[Grafana overview and stage boards](./grafana-boards.md) help locate the first stage
where work stops progressing.

## Find your symptom {#symptoms}

| Symptom | First check | Procedure |
| --- | --- | --- |
| Install or bootstrap stops before services start | Run `dcctl preflight`; read the failed step and Kubernetes events. A claimed cluster may have another operator working on it. | [Bootstrap](./bootstrap.md), [cluster lock](./cluster-lock.md), and [resuming a failed bootstrap](./disaster-recovery.md#resuming-a-bootstrap). |
| Pods are Pending, restarting, or not Ready | Describe the affected pod for scheduling, image, volume and probe failures. For a restart, inspect the previous container's logs before replacing it. | [Container restarts](./observability.md#container-restarts) and [startup refusals in the upgrade notes](/1.6/deployment/releases-and-upgrades#v160-before-upgrade). |
| After recovery, user-management refuses startup and other services never become Ready | Read user-management's startup error. A wrong root key cannot open the stored signing key; restarting does not repair it. | [Root-key recovery](./disaster-recovery.md#root-key) and [verify escrow](./disaster-recovery.md#verify). |
| Sign-in works, but API calls return 401 | Check the calling client's token audience and the receiving service's refusal logs; a token can be valid yet bound to another resource. | [Token audience checks](./observability.md#token-audience-checks). |
| A user cannot pass the second-factor step | Check clock synchronization and the account's audit records. If locked after password-authenticated guesses, reset the password before resetting the second factor. | [Second-factor lockouts](./observability.md#mfa-alerts). |
| MQTT times out or fails the TLS handshake | Check DNS, the host name on the certificate, TCP reachability and the Gateway listener's status. | [External MQTT troubleshooting](./connecting-from-outside.md#troubleshooting). |
| A device connects but telemetry is missing | Confirm the tenant and device, then inspect failed-event reasons and dropped measurements on the device's Connectivity tab. | [Why isn't my data showing up?](../guides/why-isnt-my-data-showing-up.md) and [common telemetry causes](../getting-help.md#things-that-silently-swallow-data). |
| Telemetry is delayed and backlog grows | Find the first slow reader on the stage boards; distinguish pending from in-flight messages and check the downstream database and broker. | [Consumer backlog](./observability.md#consumer-backlog), [ingest backpressure](./observability.md#ingest-backpressure), and [tuning](./tuning-an-instance.md). |
| Commands stay queued or held | Inspect the command's state, device presence, maintenance window and command channel before retrying. | [Command delivery alerts](./observability.md#command-delivery-alerts) and [sending commands](../guides/sending-commands.md). |
| A rule does not raise an alarm | Check the published profile version, the incoming metric and the detection engine's rule errors and replay state. | [Detection engine](./detection-engine.md) and [testing rules](../guides/testing-your-rules.md). |
| An alarm exists but no notification arrives | Check policy matching and channel configuration, then follow the notification stage board and its firing alerts. | [Notification channels](../guides/notification-channels.md) and [Grafana boards](./grafana-boards.md). |
| Storage fills, or a volume expansion does not reach the PVC | Check free space and growth in both databases and broker volumes; compare the requested volume size with the PVC. | [Event-store sizing and expansion](./tuning-an-instance.md#event-store-sizing) and [choosing a size](./sizing.md). |
| Backups stop shipping, or recovery is refused | Check backup alerts, each store's recovery window and root-key escrow; preserve the exact refusal. | [Backup archiving alerts](./observability.md#backup-archiving) and [disaster recovery](./disaster-recovery.md). |
| A deleted tenant is still being erased | Identify the storage system blocking progress; do not infer completion from its disappearance in the console. | [Stalled deletion](./tenant-deletion.md#stalled) and [erasure completion signals](./tenant-deletion.md#erased-signal). |

## Collect evidence before changing anything {#evidence}

Record the time range and time zone, instance, affected tenant, installed version,
last successful step and first failing step. In Grafana, select the same time range
on each board. Save the alert name and labels, not just a screenshot of its colour.

The following read-only examples use instance `acme` in namespace `dci-acme`.
Replace the namespace and pod name with yours. Run shell examples in Bash; on
Windows, use Git Bash or WSL.

```bash
dcctl version
dcctl preflight
kubectl -n dci-acme get pods
kubectl -n dci-acme get events --sort-by=.metadata.creationTimestamp
kubectl -n dci-acme describe pod YOUR_POD
kubectl -n dci-acme logs YOUR_POD --all-containers=true --since=30m
```

For a container that restarted, also request its previous logs; replace the container
name with the one from the pod description. This command reports an error if no
previous container exists.

```bash
kubectl -n dci-acme logs YOUR_POD -c YOUR_CONTAINER --previous
```

Logs may already have rotated; missing log output does not prove that no failure
occurred. Read [structured logs](./observability.md#logs) alongside Kubernetes events.
Preserve evidence before restarting pods, changing configuration or retrying a write.

:::caution Redact before sharing
Remove tokens, passwords, connection strings, root keys, escrow material and private
hostnames from logs and command output. Never attach a Kubernetes Secret or a raw
configuration dump to a public issue. Use [Getting help](../getting-help.md) for
reporting channels, including private security reports.
:::

## Routine operations {#routine-operations}

These are suggested review points, not automated schedules or new platform limits.
Choose a cadence that fits your traffic, recovery objectives and maintenance windows.

| When | Review and action | Procedure |
| --- | --- | --- |
| Each operating day and after an incident | Review firing alerts, pod restarts, pipeline backlog and free storage. Confirm alerts reach an operator; an empty panel may mean missing metrics. | [Grafana boards](./grafana-boards.md) and [alert routing](./observability.md#alert-routing). |
| Each backup cycle | Verify that both core and event stores are archiving and that their recovery windows cover your objectives. Check object content and preserved streams separately. | [Two database backups](./disaster-recovery.md#two-tiers), [backup alerts](./observability.md#backup-archiving), and [other state](./disaster-recovery.md#other-state). |
| After bootstrap or key changes, and before a recovery drill | Verify the escrow fingerprint; keep the encrypted artifact and its passphrase separately and off the cluster. | [Verify escrow](./disaster-recovery.md#verify). |
| On your recovery-drill schedule | Rehearse recovery to a fresh cluster with both stores and the original root key; verify sign-in, secret use and event history. Stream/object backup verification alone is not a restore drill. | [Recover an instance](./disaster-recovery.md#recover). |
| Before an upgrade or configuration change | Read the release's startup refusals and supported upgrade path, review the stored overlay, check backups and use a dry run before applying. | [Releases and upgrades](./releases-and-upgrades.md), [versioning policy](./versioning-policy.md#upgrade-paths), and [overlay dry run](./tuning-an-instance.md#dry-run). |
| After an upgrade or configuration change | Check rollout readiness, the boards and a representative device's ingest and command outcome; confirm backup shipping continues. | [Observability](./observability.md) and [sending commands](../guides/sending-commands.md). |
| During capacity and certificate reviews | Compare storage growth and retention with available capacity; review expiring trust anchors and plan certificate rotation before expiry. | [Sizing](./sizing.md), [tuning](./tuning-an-instance.md), and [device certificates](./device-certificates.md). |
| During access reviews and tenant offboarding | Review service accounts and roles; for deletion, verify completion and perform the operator-owned cleanup. | [Service accounts](../user-guide/service-accounts.md) and [tenant deletion](./tenant-deletion.md#operator-steps). |

The stream/object commands are **Preview**: `dcctl backup inspect --verify` checks
listed bytes, and `dcctl restore --plan` only plans. There is no restore apply command
or coordinated database checkpoint. Follow the freeze and verification requirements in
[Exporting streams and object content](./disaster-recovery.md#export-streams-objects).
