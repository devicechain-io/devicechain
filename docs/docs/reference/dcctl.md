---
sidebar_position: 4
title: dcctl Command Reference
---

# dcctl Command Reference {#dcctl-reference}

Every `dcctl` command, with its flags and defaults. This page is generated from the command definitions in the binary, so it cannot fall behind them; run `dcctl <command> --help` for the same text at a terminal.

## dcctl {#dcctl}

DeviceChain CLI.

```
dcctl [command]
```

### Commands

* [dcctl bootstrap](#dcctl-bootstrap)	 - Bootstrap a DeviceChain instance
* [dcctl broker](#dcctl-broker)	 - Message broker operations
* [dcctl dead-letters](#dcctl-dead-letters)	 - Inspect work the platform accepted and then gave up on
* [dcctl destroy](#dcctl-destroy)	 - Destroy a DeviceChain instance
* [dcctl device](#dcctl-device)	 - Act as a device
* [dcctl enable-area](#dcctl-enable-area)	 - Turn on a functional area on a running instance
* [dcctl ha](#dcctl-ha)	 - Inspect an instance's high-availability posture
* [dcctl install](#dcctl-install)	 - Prepare a cluster for DeviceChain instances
* [dcctl instances](#dcctl-instances)	 - Inspect the DeviceChain instances on this machine
* [dcctl preflight](#dcctl-preflight)	 - Check local prerequisites for installing a cluster
* [dcctl presence](#dcctl-presence)	 - Inspect and repair device presence
* [dcctl resgen](#dcctl-resgen)	 - Generate configuration resources
* [dcctl secrets](#dcctl-secrets)	 - Inspect and verify instance secret material
* [dcctl sim](#dcctl-sim)	 - Create and drive DeviceChain simulations
* [dcctl upgrade](#dcctl-upgrade)	 - Move a live instance onto a released version
* [dcctl version](#dcctl-version)	 - Get version info

## dcctl bootstrap {#dcctl-bootstrap}

Bootstrap a DeviceChain instance

### Synopsis

Provisions a usable DeviceChain instance on the given provider (e.g. "local")

```
dcctl bootstrap <provider> <instance> [flags]
```

### Options

```
      --allow-legacy-db-removal                     proceed even though this cluster still runs the pre-CloudNativePG event-store StatefulSet (dc-timescaledb-single). 🔴 This ASSERTS THAT YOU HAVE HANDLED THE DATA — it is not a migration, nothing verifies it, and applying with it set destroys that StatefulSet and brings up an empty database on the same hostname. Dump first, or use it deliberately to discard a local instance
      --build                                       build images from source into a local registry (developer path; requires source + ko)
      --cluster string                              local provider: the kind cluster 'dcctl install local' prepared (default "devicechain")
      --dev                                         local-developer preset: --build --host localhost --no-tls --yes, and implies --no-escrow (a zero-config http://localhost/ bring-up); rejects contradictory flags
      --dry-run                                     print what would happen without applying changes
      --enable-area strings                         additionally deploy a functional area on TOP of the profile (repeatable, e.g. --enable-area lwm2m-ingest --enable-area sparkplug-ingest). Composes with the cluster's --compact setting; validated against the area catalog (unknown area or unmet hard dependency fails before any cluster spin-up)
      --escrow-file string                          where to write the encrypted root-key escrow artifact (default ~/.devicechain/escrow/<instance>-rootkey.escrow). Refuses a path inside ~/.devicechain/instances/<instance>, which 'dcctl destroy' deletes
      --escrow-passphrase-file string               read the escrow passphrase from this file instead of $DCCTL_ESCROW_PASSPHRASE or an interactive prompt (trailing newline stripped)
  -h, --help                                        help for bootstrap
      --host string                                 ingress host to expose the instance on (default devicechain.local; use 'localhost' for a local cluster to skip the /etc/hosts edit)
      --kube-context string                         build the instance on the cluster this context reaches, prepared with 'dcctl install --kube-context'
      --lwm2m-identities string                     path to a JSON file of LwM2M DTLS-PSK credentials to provision: [{identity, psk(base64), tenant, externalId, deviceTypeToken, autoRegister}]. Renders the PSKs into a chart-owned Secret and binds each to lwm2m-ingest; implies --enable-area lwm2m-ingest. Validated up front (short PSK / missing tenancy fails before any cluster). Re-running bootstrap WITHOUT this flag removes the provisioned credentials
      --mqtt-fail-closed                            make a QoS 1 PUBACK from the MQTT gateway mean the platform holds the message (infrastructure.nats.mqttFailClosed). The trade: a QoS 1 publish is REFUSED (no PUBACK, connection closed, the device resends) while the broker's MQTT message store has no leader, instead of being acknowledged and possibly lost, and the ingest ceiling is lower, so size the instance below it. It covers QoS 1 only, and its guarantee is stated for a replicated broker (a cluster installed with --ha). --mqtt-fail-closed=false turns it off again. Fixed configuration, recorded on the instance and kept by 'dcctl upgrade' when the flag is omitted; not giving the flag leaves the platform default
      --no-escrow                                   do NOT escrow the secret-store root key. The key then exists only inside the cluster: losing the cluster makes every stored secret permanently unreadable, even from a database backup, because no DeviceChain backup contains etcd. For throwaway instances only; implied by --dev
      --no-tls                                      serve plain HTTP instead of a self-signed cert (with --host localhost, a zero-config http://localhost/). On by default on a cluster installed without cert-manager, where --no-tls=false is refused
      --object-store string                         where the instance keeps tenant logos and update artifacts: none, filesystem (a volume the chart creates) or s3. Default: a small filesystem store on a cluster dcctl created, none on one it was pointed at with --kube-context. Fixed configuration, recorded on the instance and kept by 'dcctl upgrade'
      --object-store-access-mode string             filesystem store: ReadWriteOnce (default) or ReadWriteMany, which needs a storage class that supports it and is required when more than one pod shares the store
      --object-store-s3-bucket string               s3 store: the bucket (required with --object-store s3)
      --object-store-s3-credentials-secret string   s3 store: <namespace>/<name> of an EXISTING Secret with keys AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY, which dcctl copies into the instance's namespace. Never the key itself: no credential is accepted on the command line or written into the instance configuration. Omit to use the pods' own AWS identity, which exists only on a cloud cluster (refused on a cluster dcctl created)
      --object-store-s3-endpoint string             s3 store: the endpoint of an S3-compatible service such as MinIO (an http:// or https:// URL without credentials)
      --object-store-s3-path-style                  s3 store: address the bucket in the URL path, which MinIO and most S3-compatible services need
      --object-store-s3-region string               s3 store: the AWS region (required unless --object-store-s3-endpoint is set)
      --object-store-size string                    filesystem store: size of the volume, a Kubernetes quantity (default 5Gi, the chart default)
      --object-store-storage-class string           filesystem store: StorageClass of the volume (default: the cluster's default class)
      --profile string                              configuration profile to apply
      --registry string                             image registry to deploy from (default: published ghcr.io/devicechain-io, or localhost:5000 with --build)
      --restore-root-key string                     disaster recovery: seed the instance's secret-store root key FROM this escrow artifact instead of minting a fresh one, so a rebuilt cluster can read secrets restored from a database backup. Needs the artifact's passphrase
      --restore-tsdb-at string                      stop the event store's recovery at this RFC3339 timestamp instead of replaying the whole archive. For the disaster where the data was destroyed correctly — a mistaken delete — so pick a moment strictly before the damage. Needs --restore-tsdb-from
      --restore-tsdb-from string                    disaster recovery: recover the EVENT store from this archive path (the serverName inside the backup bucket, e.g. dc-tsdb) instead of initialising an empty database. 🔴 Only takes effect when the cluster is CREATED — recover by destroying the instance and rebuilding it with this set, not by re-running against a live one. When the backups are in the cluster's in-cluster object store, destroy with --keep-backups: a destroy without it deletes the archive this reads
      --skip-preflight                              skip the local-system preflight checks
      --trusted-proxies strings                     CIDR ranges of the proxies whose X-Forwarded-For the services believe (infrastructure.http.trustedProxies); repeat or comma-separate. Default: the cluster's pod range, read from its nodes, which is where the ingress controller runs; add a fronting load balancer's ranges when there is one. It is recorded on the instance and kept by every upgrade; "auto" returns to deriving it from the cluster
      --values stringArray                          a YAML file of operator tuning (replica counts, resources, an area's own configuration), merged over the values dcctl computes (repeatable; later files win). The overlay is recorded on the instance, so 'dcctl upgrade' keeps applying it; on upgrade, --values REPLACES the recorded overlay. Only tunable values are accepted: a key dcctl owns (images, credentials, endpoints, ingress host and TLS, the area set) is refused, naming it, before anything changes
      --version string                              image version/tag to deploy (default: the published release version, or 'dev' with --build)
  -y, --yes                                         assume yes for prompts
```

### SEE ALSO

* [dcctl](#dcctl)	 - DeviceChain CLI

## dcctl broker {#dcctl-broker}

Message broker operations

### Synopsis

Operate on the message broker an instance runs on.

### Options

```
  -h, --help   help for broker
```

### SEE ALSO

* [dcctl](#dcctl)	 - DeviceChain CLI
* [dcctl broker permissions](#dcctl-broker-permissions)	 - Print the broker permissions every platform login is held to
* [dcctl broker rebalance](#dcctl-broker-rebalance)	 - Even out which message broker server leads which streams and consumers
* [dcctl broker reconcile](#dcctl-broker-reconcile)	 - Create and reconcile the instance's message broker streams and buckets

## dcctl broker permissions {#dcctl-broker-permissions}

Print the broker permissions every platform login is held to

### Synopsis

Print the message broker permissions of every platform login, as JSON.

An instance dcctl installs never needs this: dcctl generates the same document at install and
upgrade time and gives it to the broker. It exists for an installation that runs the chart
WITHOUT dcctl and brings its own broker, which has to hold each service to the same grants.

The document is generated from the same declarations the services are held to (which streams and
buckets each area writes, reads and administers, and under which consumer names), so it cannot
drift from what a service does. Its shape is:

```
  {"APP": {"<username>": <permissions>, ...}, "SYS": {"dc_sys": <permissions>}}
```

where each \<permissions> is a nats-server permissions block (publish and subscribe allow and deny
lists, and allow_responses). Merge each block into the user of the same name in the broker's APP
account (and dc_sys's into the system account). Usernames are dc-\<area>-g\<generation> for an area's
own login and dc-broker-admin-g\<generation> for the broker administrator. There is no shared login:
every area presents its own. An installation that was upgraded from a release that had one, and has
not been through the upgrade that retires it, passes --with-shared-login to keep the transitional
dc_service block until then.

--profile (or --enable-area) limits the output to the areas that installation deploys. A login with
no block must not be created: nats-server reads a user with no permissions as unrestricted.

```
dcctl broker permissions [flags]
```

### Examples

```
  dcctl broker permissions --instance-id acme > permissions.json
  dcctl broker permissions --instance-id acme --profile telemetry
```

### Options

```
      --enable-area stringArray   also include this area's login on top of --profile; repeat for more
  -h, --help                      help for permissions
      --instance-id string        the instance id the services run under (their DC_INSTANCE_ID); it is part of every subject and stream name (required)
      --profile string            limit the output to the areas of this deployment profile (default: every area)
      --with-shared-login         also include the transitional dc_service block, for an installation upgraded from a release that had a shared login and not yet retired it
```

### SEE ALSO

* [dcctl broker](#dcctl-broker)	 - Message broker operations

## dcctl broker rebalance {#dcctl-broker-rebalance}

Even out which message broker server leads which streams and consumers

### Synopsis

Spread the message broker's stream and consumer leaders evenly across its servers.

A rolling restart of the broker leaves the server restarted last leading nothing, and the other
servers lead everything: at the load this was measured under, publish acknowledgement latency
roughly doubled. `dcctl upgrade` does this after it rolls the broker; this runs the same step on
demand, for a broker restarted some other way (a node drain, a pod loss).

It asks the leader of each over-loaded group to hand over to a replica that is current, one
group at a time, each waiting for the new leader to be current, and moves nothing else: no data,
no configuration. A broker that is already even is left alone. Each move is a brief leader
election for one stream or consumer.

Streams that hold device data already acknowledged to the device are never moved, and there is no
option to include them: the broker's MQTT streams, the device capture stream and every stream an
ingest service writes. A leader change on them has lost messages devices had been told were safe,
so their imbalance clears at the next broker roll, or on its own.

```
dcctl broker rebalance <instance> [flags]
```

### Examples

```
  dcctl broker rebalance devicechain
  dcctl broker rebalance devicechain --kube-context kind-devicechain
```

### Options

```
  -h, --help                  help for rebalance
      --kube-context string   kube-context to target (default: the cluster recorded at bootstrap; a different context is refused)
```

### SEE ALSO

* [dcctl broker](#dcctl-broker)	 - Message broker operations

## dcctl broker reconcile {#dcctl-broker-reconcile}

Create and reconcile the instance's message broker streams and buckets

### Synopsis

Run the broker administrator again for an installed instance and wait for it.

The platform's services do not create or change the broker's streams and buckets. They verify what
they open and stop, naming what differs, when it is missing or does not match what the release
declares. One login, the broker administrator, creates and reconciles them, as a Job the chart runs
before every install and upgrade. This runs that same Job on demand, for recovery:

```
  - a pod that stops with "does not match what this build declares" or "does not exist";
  - a stream or bucket lost at runtime or deleted by hand;
  - a ceiling or replica count changed in the instance configuration and applied without a release.
```

The Job is the one the instance's last install or upgrade ran, read back from its Helm release, so
it uses the same image, the same areas and the same broker login. It is idempotent: on a broker that
already matches it changes nothing. The Job's log is printed. When it fails, the Job is left in the
instance's namespace to read and the command exits non-zero.

A stream that an older release created with a different retention is drained by its reader before
it is recreated as declared, which can take a few minutes.

```
dcctl broker reconcile <instance> [flags]
```

### Examples

```
  dcctl broker reconcile devicechain
  dcctl broker reconcile devicechain --kube-context kind-devicechain
```

### Options

```
  -h, --help                  help for reconcile
      --kube-context string   kube-context to target (default: the cluster recorded at bootstrap; a different context is refused)
```

### SEE ALSO

* [dcctl broker](#dcctl-broker)	 - Message broker operations

## dcctl dead-letters {#dcctl-dead-letters}

Inspect work the platform accepted and then gave up on

### Synopsis

Inspect work the platform accepted and then gave up on.

Four consumers record a dead letter when they have retried a message to its delivery
cap and it still cannot be completed: a detection whose actions could not be
dispatched, an alarm that reached nobody, a device's answer that could not be
recorded against its command, and an alarm edge that could not be applied.

NOTHING REPLAYS THESE. The record exists so a failure is visible and diagnosable
instead of being a log line nobody read; the consequences of the failure itself stand
either way. An alarm that was not raised was not raised.

This reads the operator plane and authenticates as an identity, not as a tenant.

### Options

```
      --email string      identity to authenticate as (required)
  -h, --help              help for dead-letters
      --otp string        one-time code (or a recovery code) for an account with two-factor authentication; used for the first sign-in only. A terminal is prompted instead when none is given
      --password string   password for the identity (required)
  -s, --server string     instance host for platform API calls (default "localhost")
      --tls               use https for platform endpoints
```

### SEE ALSO

* [dcctl](#dcctl)	 - DeviceChain CLI
* [dcctl dead-letters list](#dcctl-dead-letters-list)	 - List dead letters, newest first

## dcctl dead-letters list {#dcctl-dead-letters-list}

List dead letters, newest first

### Synopsis

List dead letters, newest first.

Narrow with --tenant, --kind, --source and --since. Reading is bounded by --pages:
when more records exist than were fetched, the output says so rather than letting a
truncated list read as a complete one.

```
dcctl dead-letters list [flags]
```

### Options

```
  -h, --help            help for list
      --kind string     only this kind: detection-action, notification, command-response, connector-dispatch, event, command, control-fact
      --page int        records per server round-trip (default 50)
      --pages int       how many pages to fetch before stopping (default 4)
      --since string    only records from this RFC3339 time onward (e.g. 2026-09-04T00:00:00Z)
      --source string   only records written by this functional area
  -t, --tenant string   only this tenant's records
```

### Options inherited from parent commands

```
      --email string      identity to authenticate as (required)
      --otp string        one-time code (or a recovery code) for an account with two-factor authentication; used for the first sign-in only. A terminal is prompted instead when none is given
      --password string   password for the identity (required)
  -s, --server string     instance host for platform API calls (default "localhost")
      --tls               use https for platform endpoints
```

### SEE ALSO

* [dcctl dead-letters](#dcctl-dead-letters)	 - Inspect work the platform accepted and then gave up on

## dcctl destroy {#dcctl-destroy}

Destroy a DeviceChain instance

### Synopsis

Removes a DeviceChain instance — the inverse of bootstrap.

This deletes the instance and ALL ITS DATA, in this order: its Helm release; its
own infrastructure — the broker and event store — by "tofu destroy" over the
instance's infrastructure state; its database and login on the shared relational
store; its namespace, waiting until it is gone; and, last, its local state under
~/.devicechain/instances/\<instance>. The root-key escrow is kept. A destroy that
fails part-way keeps the local state, and running it again resumes.

Once the namespace is gone, destroy also removes the instance's event-store
backups (WAL archive and base backups) from the cluster's in-cluster object store:
everything under the exact path the event store was archiving to, read from it
before anything was changed, and it checks that the path is empty afterwards.
Backups in an object store you supplied (install --backup-credentials-file) are
never deleted; destroy prints where they are. --keep-backups keeps in-cluster
backups too — use it when you mean to rebuild the instance from them with
"dcctl bootstrap --restore-tsdb-from", because a destroy without it deletes the
archive that restore would read. If the object store cannot be reached, destroy
still finishes and names the archive it left.

If the instance's infrastructure state is missing or empty but its broker or event
store is running, or the state cannot be read, or it still holds the cluster's shared
prerequisites (an instance built by an older dcctl), destroy refuses before changing
anything. --without-state removes such an instance anyway: it skips "tofu destroy"
and removes the instance by its Helm release, database and login, and namespace,
saying that tofu destroy was skipped and what it left on the cluster.

The cluster, and the shared prerequisites "dcctl install" put there, are never
touched — other instances may be using them, and destroy leaves the cluster
running whether or not dcctl created it. To delete a local cluster, do it by
hand: kind delete cluster --name \<name>

Which cluster the instance is in comes from a record written at bootstrap, not
from the instance's name. --kube-context may name that same context; one naming
a different context is refused, and the refusal says how to remove a record that
is wrong. With no record, --kube-context says where the instance is. An instance
bootstrapped before dcctl recorded this has no record, and with no --kube-context
destroy falls back to guessing the cluster from the instance name — saying so as
it goes. Run "dcctl instances list" to see which instances are in that state.
If the cluster is already gone, only the local state is cleared.

Use --all to destroy every instance on this machine.

```
dcctl destroy <provider> <instance> [flags]
```

### Options

```
      --all                   destroy EVERY instance on this machine (takes no arguments)
      --dry-run               print what would happen without destroying anything
  -h, --help                  help for destroy
      --keep-backups          keep the instance's event-store backups in the cluster's in-cluster object store (they are removed by default; backups in an object store you supplied are never removed). Use it to rebuild the instance from them with --restore-tsdb-from
      --kube-context string   kube-context to target (default: the cluster recorded at bootstrap; a different context is refused)
      --without-state         skip tofu destroy and remove the instance by its release, database, login and namespace (for an instance whose infrastructure state is lost)
  -y, --yes                   assume yes for prompts
```

### SEE ALSO

* [dcctl](#dcctl)	 - DeviceChain CLI

## dcctl device {#dcctl-device}

Act as a device

### Synopsis

Commands that play a device against the platform, from a workstation.

They exist to try a flow end to end before firmware does it, and to enroll a device by hand;
they are not how an operator manages devices (that is the console and the API).

### Options

```
  -h, --help   help for device
```

### SEE ALSO

* [dcctl](#dcctl)	 - DeviceChain CLI
* [dcctl device register](#dcctl-device-register)	 - Register a device through a provisioning profile, as the device would

## dcctl device register {#dcctl-device-register}

Register a device through a provisioning profile, as the device would

### Synopsis

Play a device registering itself: present a provisioning profile's key and secret
with a serial number, and receive the device's token and MQTT client id.

The device makes its own credential. This command generates it (32 random bytes, base64url) and
SAVES IT TO --material-file BEFORE SENDING anything, so a retry, a crash or a second run all
present the same credential and a registration whose answer was lost is recognised as a replay,
not refused as a second device. An existing file is reused, never overwritten. The platform keeps
only a digest of the credential, so the file is the only copy: keep it with the device.

The credential is the MQTT username "\<tenant>:\<credential>" with no password. It is printed
once, on the run that registered the device; running the command again prints the device's
details (outcome REPLAYED) and not the credential.

It follows the endpoint's client contract. On 429 and 503 it waits the Retry-After the platform
names plus a random jitter of up to 100% of it. A network error or a 5xx the platform may
recover from backs off from 1 second to 5 minutes with full jitter. Refusals that need a person
(400, 401, 403, 404, 409, 413, 422, 501, a misconfigured profile) are not retried.

The provision secret is read from --secret, from --secret-file, or from the environment
variable DCCTL_PROVISION_SECRET. Give at most one of the two flags (both is an error); either
wins over the variable. Prefer the file or the variable, which keep it out of the process
listing.

The request carries the provision secret, so it always goes over https. --insecure-plaintext
sends it over plaintext http instead, which exposes the secret to anyone on the path; it exists
for a local test cluster without a certificate. A redirect from the server is never followed
(a plain-http ingress that answers 308 would otherwise make the secret travel twice), and is
reported as an error.

The endpoint needs the instance's API ingress, and --server is that ingress host exactly as for
the other commands.

```
dcctl device register [flags]
```

### Examples

```
  dcctl device register --server dc.example.com --tenant acme \
      --key acme-sensors-2026 --secret-file ./provision.secret \
      --external-id SN-000123 --material-file ./SN-000123.credential
```

### Options

```
      --external-id string     the device's serial number or other external id (required)
  -h, --help                   help for register
      --insecure-plaintext     send the request over plaintext http, secret included; for a local test cluster only
      --key string             the provisioning profile's provision key (required)
      --material-file string   file the device's credential is saved to before it is sent, and read back on a re-run (required)
      --max-attempts int       give up after this many sends (0 keeps trying until --timeout) (default 10)
      --name string            optional device name, used only when the registration creates the device
      --secret string          the provisioning profile's provision secret; prefer --secret-file or $DCCTL_PROVISION_SECRET
      --secret-file string     file holding the provision secret
  -s, --server string          instance host for platform API calls (default "localhost")
  -t, --tenant string          tenant the device registers into (required)
      --timeout duration       give up after this long overall (0 for no limit) (default 10m0s)
```

### SEE ALSO

* [dcctl device](#dcctl-device)	 - Act as a device

## dcctl enable-area {#dcctl-enable-area}

Turn on a functional area on a running instance

### Synopsis

Deploys one or more functional areas on an instance that is already running —
for example outbound-connectors, ai-inference or mcp on an
instance bootstrapped with the default profile. It is the day-2 form of
'dcctl bootstrap --enable-area'.

The area names are checked against the area catalog before anything touches the
cluster, and the instance's resulting area set is checked for unmet dependencies
before anything is written. An unknown name or a missing dependency changes
nothing.

It records the areas in the instance's declaration, then applies the instance the
way 'dcctl upgrade' does — the instance's message broker and event store, the
configuration document its services read, and its Helm release — at the version
the instance is already on, and waits for every area to be ready. The next
'dcctl upgrade' reads the same declaration, so it keeps the areas.

It does not move the instance's version. It refuses when this dcctl belongs to a
different release than the instance's services, because it would apply this
release's chart and infrastructure configuration under the old images; upgrade
the instance first. Like 'dcctl upgrade', it needs 'tofu' on PATH and this
machine's state for the instance, and it mints no credentials.

Asking for an area the instance already runs is not an error. When every area
named is already declared and the instance's last run ended Ready, nothing is
changed. When the last run did not end Ready, it applies the instance again,
which finishes an earlier enable-area that stopped part of the way.

Some areas need configuration before they do useful work — ai-inference needs a
model provider registered by an administrator, for example. Enabling the area
deploys it; it does not configure it.

There is no command that disables an area. An enabled area may hold data, and
taking it out of the deployment would leave that data with nothing serving it.

```
  # Add the outbound connectors area to an instance
  dcctl enable-area local devicechain outbound-connectors

  # Add two areas at once, and see what would happen first
  dcctl enable-area local devicechain outbound-connectors mcp --dry-run
```

```
dcctl enable-area <provider> <instance> <area> [<area>...] [flags]
```

### Options

```
      --dry-run               print what would happen without changing anything
      --escrow-file string    where this instance's root-key escrow artifact lives (default ~/.devicechain/escrow/<instance>-rootkey.escrow). The run checks it still protects the key the instance is running on
  -h, --help                  help for enable-area
      --kube-context string   kube-context to target (default: the cluster recorded at bootstrap; a different context is refused)
```

### SEE ALSO

* [dcctl](#dcctl)	 - DeviceChain CLI

## dcctl ha {#dcctl-ha}

Inspect an instance's high-availability posture

### Options

```
  -h, --help   help for ha
```

### SEE ALSO

* [dcctl](#dcctl)	 - DeviceChain CLI
* [dcctl ha verify](#dcctl-ha-verify)	 - Assert an instance's broker actually holds the replication it declares
* [dcctl ha verify-db](#dcctl-ha-verify-db)	 - Assert a database store actually holds the replication it declares

## dcctl ha verify {#dcctl-ha-verify}

Assert an instance's broker actually holds the replication it declares

### Synopsis

Assert, from live broker state, that an instance's JetStream streams, KV
buckets, durable consumers and NATS pods are replicated the way the instance
declares.

This reads the broker, not the deployment. The failure it exists to catch is an
instance that looks highly available from every rendered artifact -- a three-node
NATS cluster, three healthy pods, the HA toggle on -- while every stream and
bucket on it is single-replica: three times the compute, zero node failures
survived. Nothing about that state is visible from Helm values, OpenTofu state,
or pod health, because all three describe what was asked for.

The declared replica factor comes from the instance-config Secret the pods mount,
so the comparison is between what this instance claims and what its broker holds.

Exit status is the result: 0 when every assertion holds, 1 when any does not.

```
  dcctl ha verify --instance default
```

Use --expect-fail to invert that, for the negative control. A check suite that
cannot fail asserts nothing, so the drill runs the SAME command against a
single-node instance and requires it to report failures:

```
  dcctl ha verify --instance default --replicas 3 --expect-fail
```

```
dcctl ha verify [flags]
```

### Options

```
      --expect-fail           invert the exit status: succeed only if the check FAILS (the negative control)
  -h, --help                  help for verify
      --instance string       instance id; its services and broker run in namespace dci-<instance> (default "default")
      --kube-context string   kubeconfig context (default: current context)
      --nats-url string       dial this NATS URL instead of opening a port-forward
      --probe-mqtt            open one MQTT connection first so the broker's $MQTT_* streams exist and their replica factor can be observed. MUTATES the broker (it creates those streams if no client has ever connected), so it is off by default
      --replicas int          override the declared replica factor (default: read from the instance's deployed configuration)
      --settle duration       keep re-checking for this long while assertions fail, for the interval in which a RAFT peer set is reconfiguring or a consumer group is remapping. Never turns a failure into a pass: on expiry the last assertion report is returned in full (if the last attempt could not collect state at all, that collection error is returned instead). 0 disables it (default 1m30s)
      --timeout duration      bound the whole check (default 5m0s)
```

### SEE ALSO

* [dcctl ha](#dcctl-ha)	 - Inspect an instance's high-availability posture

## dcctl ha verify-db {#dcctl-ha-verify-db}

Assert a database store actually holds the replication it declares

### Synopsis

Assert, from live PostgreSQL and Kubernetes state, that a CloudNativePG
database store is replicated the way it is expected to be.

This is the database sibling of "ha verify", and it exists for the same reason:
every artifact that describes the store describes what was ASKED for. A Cluster
with three ready pods, a green apply and a healthy operator can still be
replicating asynchronously -- Helm accepts a misspelled field and the Kubernetes
API server prunes it silently, so a one-character slip in a chart template
removes synchronous replication without removing anything visible.

So the checks run against the running server:

```
  the instance count and pod placement  <- the Kubernetes API
  the alias Service and its endpoints   <- the Kubernetes API
  synchronous_standby_names             <- PostgreSQL
  connected + synchronous standbys      <- PostgreSQL
```

Exit status is the result: 0 when every assertion holds, 1 when any does not.

```
  dcctl ha verify-db --cluster dc-rdb --require-synchronous
```

Use --expect-fail for the negative control, stating the topology the store is
expected NOT to hold:

```
  dcctl ha verify-db --cluster dc-rdb --instances 3 --require-synchronous --expect-fail
```

```
dcctl ha verify-db [flags]
```

### Options

```
      --alias-service string   the Service name clients use, whose selector must be the operator-maintained primary alias; empty skips that check (and says so in the report) (default "dc-postgresql")
      --cluster string         the CloudNativePG Cluster object to check (default "dc-rdb")
      --durability string      the dataDurability this store is expected to declare, checked only with --require-synchronous. Empty means "required". The event store runs "preferred" so a lost standby degrades it to asynchronous replication instead of applying backpressure to ingest, so checking it against "required" would report a correct configuration as broken
      --expect-fail            invert the exit status: succeed only if the check FAILS (the negative control)
  -h, --help                   help for verify-db
      --instances int          expected instance count (default: read from the live Cluster spec). State it explicitly for the negative control — read from the spec, a single-instance store expects one instance and duly has one, so it can never fail
      --kube-context string    kubeconfig context (default: current context)
      --namespace string       namespace holding the CloudNativePG Cluster: dc-system for the shared relational store (dc-rdb), dci-<instance> for that instance's event store (dc-tsdb) (default "dc-system")
      --require-synchronous    demand synchronous replication. Deliberately NOT derived from the Cluster spec: a pruned or misspelled field leaves a spec that asks for nothing, which a spec-derived check would happily confirm
      --settle duration        keep re-checking for this long while assertions fail, for the interval in which a failover is completing or a standby rejoining. Never turns a failure into a pass: on expiry the last assertion report is returned in full (if the last attempt could not collect state at all, that collection error is returned instead). 0 disables it (default 1m30s)
      --timeout duration       bound the whole check (default 5m0s)
      --timescale-jobs         also assert TimescaleDB background-job health across every database on the server. Continuous aggregates, retention and compression are all background jobs: if the scheduler stops, the database stays up, replicates, fails over and passes every other check here while silently no longer aggregating
```

### SEE ALSO

* [dcctl ha](#dcctl-ha)	 - Inspect an instance's high-availability posture

## dcctl install {#dcctl-install}

Prepare a cluster for DeviceChain instances

### Synopsis

Prepares a cluster once, so that any number of instances can be built on it with
"dcctl bootstrap".

For the local provider it creates a kind cluster (named by --cluster, default
"devicechain") if there is none, or uses the one that exists. --kube-context installs
into an existing cluster instead, which dcctl never creates or deletes.

It installs what every instance on the cluster shares: the DeviceChain operator and
its CRDs, the relational store and the backup object store in namespace dc-system,
and the CloudNativePG operator, cert-manager, ingress and the monitoring stack each
in a namespace of its own. It creates the base database identity each instance's own
login is made with, and records the install in the cluster. Every bootstrap follows
that record: an instance on an --ha cluster is HA, an instance on a --compact
cluster is compact.

The operator and its CRDs are the cluster's, not any instance's — there is one copy
shared by every instance, so the release a cluster is prepared at is chosen here with
--version, and moving it is this command's job rather than a side effect of building
or upgrading one instance.

--database-node-selector places the relational store, and the event store of every
instance bootstrapped on the cluster, on the nodes it names; --database-toleration lets
them onto nodes tainted to keep other workloads off.

The ingress controller is Traefik, which serves Gateway API routes. A cluster that already
runs ingress-nginx keeps it, serving, and gains Traefik beside it; a cluster that does not
gets Traefik alone. --remove-legacy-ingress removes ingress-nginx, and refuses while any
Ingress object in the cluster still names its class: move each instance first with
'dcctl upgrade --drop-legacy-ingress'.

Running it again converges. Changing its settings is refused while any instance runs on
the cluster, with one exception: the connection budget may be raised.

```
dcctl install <provider> [flags]
```

### Options

```
      --allow-legacy-db-removal          proceed even though this cluster still runs the pre-CloudNativePG relational StatefulSet (dc-postgresql). 🔴 This ASSERTS THAT YOU HAVE HANDLED THE DATA — applying with it set destroys that StatefulSet and brings up an empty database on the same hostname
      --aws-nlb string                   EKS: which ingress controllers' LoadBalancer Services get an AWS Network Load Balancer (service.beta.kubernetes.io/aws-load-balancer-type: nlb) instead of the Classic one a plain Service gets: traefik, nginx, all, or none (turns it off). Only a Network Load Balancer passes the client's address through, which the per-source rate limits and the trusted-proxy rule depend on. Left off, each controller's Service keeps what it has. A value that CHANGES an existing Service replaces its load balancer and so changes the hostname clients resolve: repoint DNS afterwards (the install says so). traefik alone leaves ingress-nginx's Service as it is, for a cluster mid-cutover
      --backup-credentials-file string   send database backups to an object store you already own, described by this JSON file: {endpointUrl, bucketRdb, bucketTsdb, accessKeyId, secretAccessKey}. Without it the cluster provisions its own in-cluster store, which lives in the same failure domain as the databases it backs up. Keep the file readable only by you
      --backup-snapshot-class string     take each database's daily base backup as a CSI volume snapshot with this VolumeSnapshotClass instead of a full copy in the backup store. A full copy still goes to the store weekly, WAL archiving is unchanged, and restores still read the store. The class must exist, use deletionPolicy Delete, and belong to the driver that provisions the database volumes. Every instance on the cluster follows it
      --build                            developer path: ko-build the operator image from this source checkout and push it to a local registry, instead of pulling a published one. Builds ONLY the operator; the service images are an instance's and are built by dcctl bootstrap --build
      --cluster string                   local provider: the kind cluster to create or use (default "devicechain")
      --compact                          small-footprint preset for the cluster and every instance on it: smaller volumes, lowered JetStream/KV ceilings and scheduling requests, no monitoring stack, and — unless --no-tls=false — no cert-manager and therefore no database backups. Instances on a compact cluster keep the default profile or a smaller one
      --database-node-selector strings   run the databases only on nodes with this label, as key=value (repeatable; every label must match). Applies to the relational store and to the event store of every instance bootstrapped on the cluster. Refused when too few schedulable nodes match
      --database-toleration strings      let the databases run on nodes with this taint, written as kubectl taint writes it: key=value:Effect, or key:Effect for any value (repeatable). Needs --database-node-selector
      --dev                              local-developer preset: --build --yes (builds the operator image from this source checkout); rejects contradictory flags
      --dry-run                          print what would happen without applying changes
      --ha                               a replicated relational store (3 CloudNativePG instances, synchronous), and every instance bootstrapped on this cluster HA too: a 3-server NATS cluster with replicated streams and a replicated event store. Needs at least 3 schedulable nodes; database volumes are sized per instance
  -h, --help                             help for install
      --kube-context string              install into this existing cluster instead of a local kind cluster (never created or deleted by dcctl)
      --max-connections int              the relational store's connection budget (default 700 on a first install, and what the cluster has on a re-run). Each instance reserves (its relational services x 40) of it when bootstrapped; the default admits two default-profile instances
      --no-cnpg                          skip the CloudNativePG operator and the database backup plugin — for a cluster that ALREADY runs CNPG, since Helm cannot adopt objects another installer created
      --no-monitoring                    skip the monitoring stack (Prometheus/Grafana); instances on the cluster render no ServiceMonitors or alerts
      --no-tls                           with --compact: instances serve plain HTTP, so cert-manager is not installed and database backups (whose plugin needs it) are off. --compact --no-tls=false keeps both
      --registry string                  pull the operator image from this registry (default: the published registry, or the local one with --build)
      --remove-legacy-ingress            remove ingress-nginx from a cluster that still runs it. An install otherwise leaves it serving and installs Traefik beside it. Refused while any Ingress object in the cluster still names its class (each instance must first be moved with 'dcctl upgrade --drop-legacy-ingress'); on a local cluster Traefik takes the node's ports 80 and 443 in the same apply
      --restore-rdb-at string            stop the relational store's recovery at this RFC3339 timestamp instead of replaying the whole archive. For the disaster where the data was destroyed correctly — a bad migration, a mistaken delete — so pick a moment strictly before the damage. Needs --restore-rdb-from
      --restore-rdb-from string          disaster recovery: recover the RELATIONAL store from this archive path (the serverName inside the backup bucket, e.g. dc-rdb) instead of initialising an empty database. 🔴 Only takes effect when the store is CREATED — recover by installing into a cluster whose relational store is not there, not by re-running against a live one
      --skip-preflight                   skip the local-system preflight checks
      --version string                   install the operator at this released tag (default: the release this dcctl was built for). This is the CLUSTER's version — the instances on it carry their own
  -y, --yes                              assume yes for prompts
```

### SEE ALSO

* [dcctl](#dcctl)	 - DeviceChain CLI

## dcctl instances {#dcctl-instances}

Inspect the DeviceChain instances on this machine

### Options

```
  -h, --help   help for instances
```

### SEE ALSO

* [dcctl](#dcctl)	 - DeviceChain CLI
* [dcctl instances break-glass](#dcctl-instances-break-glass)	 - Recover an operator account without signing in, with cluster credentials
* [dcctl instances list](#dcctl-instances-list)	 - List the instances bootstrapped on this machine and the clusters they live in
* [dcctl instances reclaim](#dcctl-instances-reclaim)	 - Take the cluster lock from an operator whose run has stopped
* [dcctl instances release](#dcctl-instances-release)	 - Remove an instance declaration without destroying the instance

## dcctl instances break-glass {#dcctl-instances-break-glass}

Recover an operator account without signing in, with cluster credentials

### Options

```
  -h, --help   help for break-glass
```

### SEE ALSO

* [dcctl instances](#dcctl-instances)	 - Inspect the DeviceChain instances on this machine
* [dcctl instances break-glass mfa-reset](#dcctl-instances-break-glass-mfa-reset)	 - Remove an operator's second factor and replace its password, from inside a pod

## dcctl instances break-glass mfa-reset {#dcctl-instances-break-glass-mfa-reset}

Remove an operator's second factor and replace its password, from inside a pod

### Synopsis

Removes the two-factor authentication from an identity that holds a system role, replaces
its password, and ends every session it has. For the operator who cannot sign in: an enrolled
superuser who lost the phone and the recovery codes has no one the admin API could reset them.

It does not talk to the admin API and needs no sign-in. It runs the service's own binary inside a
ready user-management pod of the instance (`dc-user-management break-glass mfa-reset`), so it needs the
cluster credentials to exec into that pod (pods/exec in the instance namespace), and nothing else. The
reset is the store code the service itself runs; this command carries no SQL.

The password is ALWAYS replaced, because this route has no caller to vouch for: removing the factor
alone would hand the account to whoever holds the old password. dcctl generates the new password
itself, sends it to the pod on stdin and prints it ONCE, here. For automation, or to choose the
password yourself, use --password-stdin: the first line of stdin is the password, and nothing is
printed. If the identity is the instance's seed superuser, dcctl also updates the password key of the
instance Secret that dcctl sim, the probes and a bootstrap re-run read (only that key), and warns
loudly if it cannot, since the reset has already happened by then.

It refuses an identity that holds no system role. Reset anyone else through the admin API
(resetIdentityMfa), where the log names the operator who did it. The reset is recorded as mfa_reset
by "break-glass:pod-exec".

```
dcctl instances break-glass mfa-reset [email] [flags]
```

### Options

```
      --email string          the operator identity's address (or give it as the argument)
  -h, --help                  help for mfa-reset
      --instance string       instance whose pod to run in (required)
      --kube-context string   kube context of the cluster the instance runs in (default: the one recorded for the instance, else the current one)
      --password-stdin        read the new password as one line from stdin instead of generating one
  -y, --yes                   skip the confirmation prompt
```

### SEE ALSO

* [dcctl instances break-glass](#dcctl-instances-break-glass)	 - Recover an operator account without signing in, with cluster credentials

## dcctl instances list {#dcctl-instances-list}

List the instances bootstrapped on this machine and the clusters they live in

### Synopsis

Lists every instance dcctl has state for, the cluster it was bootstrapped into,
and whether that cluster is still there.

The cluster column is read from a record written at bootstrap. An instance created
before dcctl recorded that shows as "no record" — destroy still works on it, but it
falls back to guessing the cluster from the instance name, which is wrong for any
instance bootstrapped with --kube-context.

A row reading "cluster gone" is an instance whose cluster has been deleted out from
under it, leaving only local state. That is the normal end state of a validation rig
run, and clearing it is what `dcctl destroy` does.

```
dcctl instances list [flags]
```

### Options

```
  -h, --help   help for list
```

### SEE ALSO

* [dcctl instances](#dcctl-instances)	 - Inspect the DeviceChain instances on this machine

## dcctl instances reclaim {#dcctl-instances-reclaim}

Take the cluster lock from an operator whose run has stopped

### Synopsis

Takes the lock a dcctl run holds while it applies to a cluster.

A run holds the lock and renews it continuously. If the process is killed without
being able to give it back — a lost laptop, a dead SSH session, an OOM — the lock
stays until somebody takes it.

This command refuses until the lock has gone a full lease duration with nothing
touching it, which it verifies by watching rather than by comparing timestamps, so
that two machines whose clocks disagree cannot produce a wrong answer. That check
takes about a minute and there is no way to skip it.

🔴 It cannot tell a dead process from one that is merely stopped — a suspended VM
or a closed laptop lid looks exactly the same from here, and no amount of waiting
changes that. Confirm the other process is really gone before you take its lock.
You will be asked to type the holder's identity back, which is there to make you
read it.

```
dcctl instances reclaim [flags]
```

### Options

```
  -h, --help                  help for reclaim
      --kube-context string   kube context of the cluster whose lock to take (required off the machine that bootstrapped it)
```

### SEE ALSO

* [dcctl instances](#dcctl-instances)	 - Inspect the DeviceChain instances on this machine

## dcctl instances release {#dcctl-instances-release}

Remove an instance declaration without destroying the instance

### Synopsis

Removes the finalizer from an instance declaration and deletes it.

🔴 THIS DESTROYS NOTHING. The namespaces, the databases, the volumes and the
workloads are all still there afterwards, and dcctl will no longer have a
declaration describing them. That is the point: it is the escape hatch for a
declaration that cannot be deleted because the operator that would clear its
finalizer is gone.

If what you want is to remove the instance, use `dcctl destroy`, which
clears the finalizer itself as its last step.

```
dcctl instances release <instance> [flags]
```

### Options

```
  -h, --help                  help for release
      --kube-context string   kube context of the cluster holding the declaration
      --yes                   skip the confirmation prompt
```

### SEE ALSO

* [dcctl instances](#dcctl-instances)	 - Inspect the DeviceChain instances on this machine

## dcctl preflight {#dcctl-preflight}

Check local prerequisites for installing a cluster

### Synopsis

Proactively diagnoses local-system prerequisites (tools, docker, kernel limits, disk, ports, kube contexts) before 'dcctl install' prepares a cluster, with an actionable fix for each problem found.

These are the prerequisites for standing a cluster up, not for building an instance on one: a bootstrap runs against a cluster 'dcctl install' has already prepared, so run this before the install.

```
dcctl preflight [provider] [flags]
```

### Options

```
      --build                 check for building images from source (needs Docker even on an existing cluster)
      --cluster string        local provider: the kind cluster the run would create or use (default "devicechain")
  -h, --help                  help for preflight
      --kube-context string   check for installing into this existing cluster instead of a local kind cluster
```

### SEE ALSO

* [dcctl](#dcctl)	 - DeviceChain CLI

## dcctl presence {#dcctl-presence}

Inspect and repair device presence

### Synopsis

Operate on the live presence projection of a tenant's devices.

A device's presence is either INFERRED — derived from its own traffic, and swept to
offline after a period of silence — or ASSERTED, meaning some event source is telling
the platform directly whether that device is connected.

An asserted device is judged only by that source. If the source stops running, the
platform stops hearing about the device and neither of the two mechanisms that would
ordinarily repair a stale row can touch it: the inactivity sweep skips asserted rows,
and a data event cannot flip one. The device keeps whatever presence it last had, for
as long as the source stays away.

'presence demote' is the way back: it releases a source's custody of its devices and
hands them to inference again.

### Options

```
      --email string      identity to authenticate as; it must be a member of the tenant (required)
  -h, --help              help for presence
      --otp string        one-time code (or a recovery code) for an account with two-factor authentication; used for the first sign-in only. A terminal is prompted instead when none is given
      --password string   password for the identity (required)
  -s, --server string     instance host for platform API calls (default "localhost")
  -t, --tenant string     tenant whose devices are affected (required)
      --tls               use https for platform endpoints
```

### SEE ALSO

* [dcctl](#dcctl)	 - DeviceChain CLI
* [dcctl presence demote](#dcctl-presence-demote)	 - Return an event source's asserted devices to inferred presence

## dcctl presence demote {#dcctl-presence-demote}

Return an event source's asserted devices to inferred presence

### Synopsis

Release an event source's custody of every device it asserts presence for,
returning those devices to inferred presence.

Use this when a presence source has stopped running and its devices are frozen. A
device that was connected when the source went away reads connected forever — the
console, agents and rules all report a live device, and commands are dispatched into a
transport that drops them. A device that was disconnected has its commands held, and
those held commands count against the tenant's undelivered ceiling, so one wedged
device can block enqueues for healthy ones.

Demoting asserts NOTHING about connectivity. It does not mark anything offline: it
changes who decides. A stale-online device becomes eligible for the inactivity sweep
again and is judged on its own traffic; a wedged-offline device is reconnected by its
next event.

THE BLAST RADIUS IS AN ENTIRE EVENT SOURCE unless you narrow it with --device, so
--source is required and is never guessed. Pass it exactly as a device's reported
source: for MQTT and HTTP that is the event source's own configured id ("mqtt1",
"http1"), for Sparkplug "sparkplug:\<hostId>", for LwM2M "lwm2m". A source nobody uses
is not an error — it simply matches nothing — so a misspelling looks exactly like a
finished run, and this command says so when the first page matches no rows.

The server answers one page at a time; this command walks the whole source and reports
the totals. It is safe to re-run: an interrupted walk resumes by running it again, and
a device that is already inferred is not matched a second time.

Start with --dry-run, which writes nothing and lists the devices in scope.

```
dcctl presence demote [flags]
```

### Options

```
      --device stringArray   limit the run to this device token, within the source; repeat for more (default: the whole source)
      --dry-run              list the devices in scope and write nothing
  -h, --help                 help for demote
      --page int             devices per server round-trip, 1-1000 (default 200)
      --reason string        why this source is being released; recorded on every event written (required)
      --source string        event source whose devices are released, exactly as they report it (required)
  -y, --yes                  assume yes for prompts
```

### Options inherited from parent commands

```
      --email string      identity to authenticate as; it must be a member of the tenant (required)
      --otp string        one-time code (or a recovery code) for an account with two-factor authentication; used for the first sign-in only. A terminal is prompted instead when none is given
      --password string   password for the identity (required)
  -s, --server string     instance host for platform API calls (default "localhost")
  -t, --tenant string     tenant whose devices are affected (required)
      --tls               use https for platform endpoints
```

### SEE ALSO

* [dcctl presence](#dcctl-presence)	 - Inspect and repair device presence

## dcctl resgen {#dcctl-resgen}

Generate configuration resources

### Synopsis

Generates configuration resources directly from the microservice codebase.

Writes the default instance configuration as a YAML file under ./resources (created in
the current directory if it does not exist), named \<group>_\<name>.yaml. The files are
rendered from the same typed configuration the services validate at startup, so they
are a starting point for an instance declaration that is known to parse.

```
dcctl resgen [flags]
```

### Options

```
  -h, --help   help for resgen
```

### SEE ALSO

* [dcctl](#dcctl)	 - DeviceChain CLI

## dcctl secrets {#dcctl-secrets}

Inspect and verify instance secret material

### Options

```
  -h, --help   help for secrets
```

### SEE ALSO

* [dcctl](#dcctl)	 - DeviceChain CLI
* [dcctl secrets escrow](#dcctl-secrets-escrow)	 - Work with root-key escrow artifacts

## dcctl secrets escrow {#dcctl-secrets-escrow}

Work with root-key escrow artifacts

### Synopsis

The instance secret-store root key wraps every stored secret. Its only copy inside the
cluster lives in etcd, which no DeviceChain backup contains — so `dcctl bootstrap` writes an
encrypted escrow artifact, and these commands are how you check that artifact is the right
one BEFORE the day you need it.

### Options

```
  -h, --help   help for escrow
```

### SEE ALSO

* [dcctl secrets](#dcctl-secrets)	 - Inspect and verify instance secret material
* [dcctl secrets escrow show](#dcctl-secrets-escrow-show)	 - Print what an escrow artifact says about itself (no passphrase needed)
* [dcctl secrets escrow verify](#dcctl-secrets-escrow-verify)	 - Check that an escrow artifact holds the root key a live instance is running

## dcctl secrets escrow show {#dcctl-secrets-escrow-show}

Print what an escrow artifact says about itself (no passphrase needed)

### Synopsis

Print the cleartext header of a root-key escrow artifact: the instance it belongs to, when it
was created, its cipher and key-derivation parameters, and the digest of the root key it holds.

Everything printed is cleartext and needs no passphrase; nothing is decrypted. The header is
bound to the encrypted key, so an edit to it fails at restore, but show does not check that. Use it to tell which of several files belongs to which instance. To check an
artifact against a live instance, use "dcctl secrets escrow verify".

```
dcctl secrets escrow show <file> [flags]
```

### Options

```
  -h, --help   help for show
```

### SEE ALSO

* [dcctl secrets escrow](#dcctl-secrets-escrow)	 - Work with root-key escrow artifacts

## dcctl secrets escrow verify {#dcctl-secrets-escrow-verify}

Check that an escrow artifact holds the root key a live instance is running

### Synopsis

Check that a root-key escrow artifact holds the root key a live instance is running.

The check compares key digests: the artifact's against the digest of the key in the instance's
configuration Secret (read through --kube-context, or the current context). It needs no
passphrase and decrypts nothing, so it is safe to run as often as you like, from a scheduled
job or a CI gate.

A mismatch means the artifact is intact but belongs to a different key (usually the instance
was re-bootstrapped after the file was written, or the file belongs to another instance), so
the instance has no usable escrow. The command exits non-zero in that case.

```
  dcctl secrets escrow verify --instance prod ./prod-rootkey.escrow
```

```
dcctl secrets escrow verify <file> [flags]
```

### Options

```
  -h, --help                  help for verify
      --instance string       instance id to check the artifact against (required)
      --kube-context string   kube-context to target (default: current)
```

### SEE ALSO

* [dcctl secrets escrow](#dcctl-secrets-escrow)	 - Work with root-key escrow artifacts

## dcctl sim {#dcctl-sim}

Create and drive DeviceChain simulations

### Synopsis

Create and drive standalone DeviceChain simulations.

'sim create' mints a scoped per-sim identity + tenant on the instance and writes a
handshake file the dc-simulator process reads to come up. 'sim start/stop/status'
drive an already-running sim through its control API; 'sim destroy' tears the
scoped identity and the tenant back down.

The superuser password comes from --admin-password, else $DC_ADMIN_PASSWORD, else the
instance's own Secret on the cluster (read through --kube-context, or the context the
instance was bootstrapped on).

### Options

```
      --admin-email string      superuser identity that mints the scoped sim identity (default "superuser@devicechain.local")
      --admin-password string   superuser password (default: $DC_ADMIN_PASSWORD, else read from the instance's Secret on the cluster)
      --control-addr string     the dc-simulator process's control API address (default "http://localhost:8090")
  -h, --help                    help for sim
  -i, --instance string         instance id (the literal event-ingress path segment; must match the deployed instance.id) (default "devicechain")
      --kube-context string     kube-context to read the superuser password from when none is given (default: the one the instance was bootstrapped on)
      --otp string              one-time code (or a recovery code) for an account with two-factor authentication; used for the first sign-in only. A terminal is prompted instead when none is given
  -s, --server string           instance host for platform API calls (default "localhost")
      --tls                     use https/wss for platform endpoints
```

### SEE ALSO

* [dcctl](#dcctl)	 - DeviceChain CLI
* [dcctl sim create](#dcctl-sim-create)	 - Mint a scoped identity + tenant for a sim and write its handshake
* [dcctl sim destroy](#dcctl-sim-destroy)	 - Delete a sim's scoped identity and its tenant
* [dcctl sim run](#dcctl-sim-run)	 - Run the dc-simulator process for a sim, in the foreground
* [dcctl sim start](#dcctl-sim-start)	 - Start (resume) a stopped sim's emit loop
* [dcctl sim status](#dcctl-sim-status)	 - Show a running sim's lifecycle state
* [dcctl sim stop](#dcctl-sim-stop)	 - Stop a running sim's emit loop (tenant + data are kept)

## dcctl sim create {#dcctl-sim-create}

Mint a scoped identity + tenant for a sim and write its handshake

### Synopsis

Mint a scoped per-sim identity and tenant on the instance, then write the
handshake file the dc-simulator actor reads to come up.

dcctl is the ONLY caller of the instance admin surface: it logs in as the superuser,
creates the sim's tenant, creates an identity with NO system roles, and binds it to
that tenant with the tenant-admin role (full authority scoped to that one tenant).
The sim actor authenticates as that identity and never touches the admin surface.

Idempotent: re-running create against an existing sim reconciles it (and keeps the
stored password in sync with the identity).

```
dcctl sim create <name> [flags]
```

### Options

```
  -h, --help                 help for create
      --ingress string       device-plane HTTP ingress base URL (default http(s)://<server>:8081)
      --manifest string      built-in scenario to run (devicepulse, buildingpulse, widgetlab, sitepulse, fleetpulse, towerpulse) (default "devicepulse")
      --mqtt-broker string   NATS MQTT gateway a scenario's command far end dials (default ssl://<server>:1883)
      --mqtt-insecure        skip verification of the MQTT gateway's certificate (defaults ON without --tls, since a local bring-up's gateway cert is self-signed; pass --mqtt-insecure=false to force verification)
      --seed int             deterministic generation seed for the sim's populations (default 1)
      --shed-priority int    shed-priority override 1-100 (0 = inherit the tier's); a load-test lever to place a probe tenant in a shed band
      --tier string          tenant tier to package the sim at (default "silver")
```

### Options inherited from parent commands

```
      --admin-email string      superuser identity that mints the scoped sim identity (default "superuser@devicechain.local")
      --admin-password string   superuser password (default: $DC_ADMIN_PASSWORD, else read from the instance's Secret on the cluster)
      --control-addr string     the dc-simulator process's control API address (default "http://localhost:8090")
  -i, --instance string         instance id (the literal event-ingress path segment; must match the deployed instance.id) (default "devicechain")
      --kube-context string     kube-context to read the superuser password from when none is given (default: the one the instance was bootstrapped on)
      --otp string              one-time code (or a recovery code) for an account with two-factor authentication; used for the first sign-in only. A terminal is prompted instead when none is given
  -s, --server string           instance host for platform API calls (default "localhost")
      --tls                     use https/wss for platform endpoints
```

### SEE ALSO

* [dcctl sim](#dcctl-sim)	 - Create and drive DeviceChain simulations

## dcctl sim destroy {#dcctl-sim-destroy}

Delete a sim's scoped identity and its tenant

### Synopsis

Tear a sim back down: delete its scoped identity (with its membership), then its
tenant, then the local sim record.

The tenant goes with the sim, unconditionally. A sim tenant exists only to hold that
sim's entities, and the identity 'sim create' minted is the only member it is ever
given — so a kept tenant is one that appears in nobody's tenant menu, reachable only
by a superuser breaking glass into it through the API. (This used to be opt-in behind
--purge, whose default claimed to keep the tenant "for inspection" while deleting the
membership that made inspection routine.)

Teardown order is enforced: the identity goes first, because the server refuses a
tenant delete while any membership still references it.

🔴 THE NAME STAYS TAKEN UNTIL THE PURGE FINISHES. 'dcctl sim create \<name>' refuses a
name whose tenant is still being deleted, and that refusal is the point: the tenant
token is the key every functional area stores its rows under, so a sim recreated at a
name whose data is still there would attach to the previous run's devices, dashboards
and telemetry instead of starting clean. Once every area reports its rows reclaimed the
name is free again.

What this does NOT delete, yet: those rows in the other functional areas. Nothing
cascades a tenant delete to them today, so they are kept (telemetry too, unless the
instance opted in to a retention policy) until the tenant purge reclaims them — which
means, until that sweep exists, the name does not come back.

Human access ends at once: no membership remains and no token can be minted. The DEVICE
plane is cut too, but it drains rather than stopping dead, and the difference matters if
you are watching a dashboard. New connects, ingest and command dispatch are refused
within about a minute (the refusal is a cached read, refreshed on that interval). A
session already established keeps its broker-issued credential until it expires — up to
12 hours by default — though its ingest is refused at admission regardless. And a device
still holding a lifetime or a subscription drops off on its own schedule, so presence can
read connected for a while after the data stops.

```
dcctl sim destroy <name> [flags]
```

### Options

```
  -h, --help   help for destroy
```

### Options inherited from parent commands

```
      --admin-email string      superuser identity that mints the scoped sim identity (default "superuser@devicechain.local")
      --admin-password string   superuser password (default: $DC_ADMIN_PASSWORD, else read from the instance's Secret on the cluster)
      --control-addr string     the dc-simulator process's control API address (default "http://localhost:8090")
  -i, --instance string         instance id (the literal event-ingress path segment; must match the deployed instance.id) (default "devicechain")
      --kube-context string     kube-context to read the superuser password from when none is given (default: the one the instance was bootstrapped on)
      --otp string              one-time code (or a recovery code) for an account with two-factor authentication; used for the first sign-in only. A terminal is prompted instead when none is given
  -s, --server string           instance host for platform API calls (default "localhost")
      --tls                     use https/wss for platform endpoints
```

### SEE ALSO

* [dcctl sim](#dcctl-sim)	 - Create and drive DeviceChain simulations

## dcctl sim run {#dcctl-sim-run}

Run the dc-simulator process for a sim, in the foreground

### Synopsis

Run the dc-simulator process for a sim that 'sim create' made, in the foreground,
with that sim's handshake file and the control port 'sim status/start/stop' reach.

dc-simulator is looked up on PATH unless --simulator names it. Interrupt (Ctrl+C) stops
it gracefully.

--play-devices (sitepulse only) makes the simulator play the device plane itself, the
scene's machine model driving the scene's fleet tracks, instead of a Unity player. Do not
run a player against the same sim while it is on.

```
dcctl sim run <name> [flags]
```

### Options

```
      --emit-interval duration   override the telemetry cadence, e.g. 3s (0 keeps the scenario's own)
  -h, --help                     help for run
      --play-devices             sitepulse only: play the device plane in the simulator instead of a Unity player
      --publish-log string       with --play-devices: append every event the ingress accepted to this NDJSON file
      --simulator string         path to the dc-simulator binary (default: dc-simulator on PATH)
```

### Options inherited from parent commands

```
      --admin-email string      superuser identity that mints the scoped sim identity (default "superuser@devicechain.local")
      --admin-password string   superuser password (default: $DC_ADMIN_PASSWORD, else read from the instance's Secret on the cluster)
      --control-addr string     the dc-simulator process's control API address (default "http://localhost:8090")
  -i, --instance string         instance id (the literal event-ingress path segment; must match the deployed instance.id) (default "devicechain")
      --kube-context string     kube-context to read the superuser password from when none is given (default: the one the instance was bootstrapped on)
      --otp string              one-time code (or a recovery code) for an account with two-factor authentication; used for the first sign-in only. A terminal is prompted instead when none is given
  -s, --server string           instance host for platform API calls (default "localhost")
      --tls                     use https/wss for platform endpoints
```

### SEE ALSO

* [dcctl sim](#dcctl-sim)	 - Create and drive DeviceChain simulations

## dcctl sim start {#dcctl-sim-start}

Start (resume) a stopped sim's emit loop

```
dcctl sim start <name> [flags]
```

### Options

```
  -h, --help   help for start
```

### Options inherited from parent commands

```
      --admin-email string      superuser identity that mints the scoped sim identity (default "superuser@devicechain.local")
      --admin-password string   superuser password (default: $DC_ADMIN_PASSWORD, else read from the instance's Secret on the cluster)
      --control-addr string     the dc-simulator process's control API address (default "http://localhost:8090")
  -i, --instance string         instance id (the literal event-ingress path segment; must match the deployed instance.id) (default "devicechain")
      --kube-context string     kube-context to read the superuser password from when none is given (default: the one the instance was bootstrapped on)
      --otp string              one-time code (or a recovery code) for an account with two-factor authentication; used for the first sign-in only. A terminal is prompted instead when none is given
  -s, --server string           instance host for platform API calls (default "localhost")
      --tls                     use https/wss for platform endpoints
```

### SEE ALSO

* [dcctl sim](#dcctl-sim)	 - Create and drive DeviceChain simulations

## dcctl sim status {#dcctl-sim-status}

Show a running sim's lifecycle state

```
dcctl sim status <name> [flags]
```

### Options

```
  -h, --help   help for status
```

### Options inherited from parent commands

```
      --admin-email string      superuser identity that mints the scoped sim identity (default "superuser@devicechain.local")
      --admin-password string   superuser password (default: $DC_ADMIN_PASSWORD, else read from the instance's Secret on the cluster)
      --control-addr string     the dc-simulator process's control API address (default "http://localhost:8090")
  -i, --instance string         instance id (the literal event-ingress path segment; must match the deployed instance.id) (default "devicechain")
      --kube-context string     kube-context to read the superuser password from when none is given (default: the one the instance was bootstrapped on)
      --otp string              one-time code (or a recovery code) for an account with two-factor authentication; used for the first sign-in only. A terminal is prompted instead when none is given
  -s, --server string           instance host for platform API calls (default "localhost")
      --tls                     use https/wss for platform endpoints
```

### SEE ALSO

* [dcctl sim](#dcctl-sim)	 - Create and drive DeviceChain simulations

## dcctl sim stop {#dcctl-sim-stop}

Stop a running sim's emit loop (tenant + data are kept)

```
dcctl sim stop <name> [flags]
```

### Options

```
  -h, --help   help for stop
```

### Options inherited from parent commands

```
      --admin-email string      superuser identity that mints the scoped sim identity (default "superuser@devicechain.local")
      --admin-password string   superuser password (default: $DC_ADMIN_PASSWORD, else read from the instance's Secret on the cluster)
      --control-addr string     the dc-simulator process's control API address (default "http://localhost:8090")
  -i, --instance string         instance id (the literal event-ingress path segment; must match the deployed instance.id) (default "devicechain")
      --kube-context string     kube-context to read the superuser password from when none is given (default: the one the instance was bootstrapped on)
      --otp string              one-time code (or a recovery code) for an account with two-factor authentication; used for the first sign-in only. A terminal is prompted instead when none is given
  -s, --server string           instance host for platform API calls (default "localhost")
      --tls                     use https/wss for platform endpoints
```

### SEE ALSO

* [dcctl sim](#dcctl-sim)	 - Create and drive DeviceChain simulations

## dcctl upgrade {#dcctl-upgrade}

Move a live instance onto a released version

### Synopsis

Upgrades an existing instance: its message broker and event store, applied
from this release's OpenTofu configuration; the configuration document its
services read; and the Helm release that runs them.

A DeviceChain release is one version across the service images, the Helm chart,
the operator and dcctl — but this command moves ONE INSTANCE, not the cluster.
The operator and its CRDs are cluster-scoped: there is one copy, shared by every
instance on the cluster. Moving them is 'dcctl install', so that a cluster-wide
change happens when somebody asks for one, rather than as a side effect of
upgrading whichever instance came first.

So an upgrade is two commands, in this order:

```
  dcctl install <provider> --version <tag>     # once, moves the cluster
  dcctl upgrade <provider> <instance> --version <tag>   # per instance
```

This command checks the cluster's operator and never moves it. It REFUSES when the
cluster has no operator, or has one identifiably from another release, naming the
install command that fixes it. An operator installed by hand carries no record of
which release put it there, and dcctl cannot tell that apart from one an older
dcctl overwrote — so that case is allowed through with a note rather than refused.

It mints no credentials. Every credential the instance is running on is read back
and kept: the database passwords, the broker's authority and logins, the
cross-service secret and the secret-store root key. A version change here cannot
become a credential change.

The one thing it removes is the shared message broker login of an instance upgraded
from a release that had one. The upgrade that gives every area a login of its own
keeps the shared one, so pods that have not restarted yet stay connected. Run the
upgrade once more: when every area already runs its own login, it drops the shared
login from the broker and from the instance configuration, and its closing summary
says which phase ran. A new instance never has the shared login. Once it is gone,
rolling back across this release is not supported; run the newer upgrade again.

It does not change an instance's shape. The profile, topology and functional
areas come from the instance's own declaration, which is what 'dcctl bootstrap'
recorded. Use it to move versions, not to reconfigure.

The fail-closed MQTT acknowledgement is the other: '--mqtt-fail-closed' (or
'--mqtt-fail-closed=false') sets it on a live instance, and the declaration records the
choice. A plain upgrade with no flag keeps whatever was recorded, so an instance built
with it stays on, and one told off stays off.

Where the cluster runs Traefik (the Gateway API controller that replaces ingress-nginx) an
upgrade moves the instance's routing to a Gateway and HTTPRoutes. While ingress-nginx is still
on the cluster the instance's Ingress objects are rendered beside them under a certificate
Secret of their own, so the host keeps answering where it did until DNS points at the new
controller; '--drop-legacy-ingress' then stops rendering them, and 'dcctl install
--remove-legacy-ingress' removes ingress-nginx once no instance has one. '--ingress-controller'
chooses the front end by hand, records the choice on the instance, and 'nginx' is the way back
that later upgrades keep ('--ingress-controller auto' returns to deciding from the cluster). The
Gateway names a new certificate Secret while the Ingress objects remain, so the host DNS still
reaches never waits for a certificate; the upgrade waits for the Gateway's own to be Ready.

Operator tuning is the exception, and it is carried: a values overlay given to
'dcctl bootstrap --values' or to this command is recorded on the instance and applied
again by every later upgrade. '--values \<file>' replaces the recorded overlay,
'--reset-values' removes it, and --dry-run prints what would change in it.

The broker and event store are applied from this machine's OpenTofu state for the
instance, in ~/.devicechain/instances/\<instance>/, so the upgrade needs 'tofu' on
PATH and runs where the instance was bootstrapped. It plans first, prints what
changes, and REFUSES — changing nothing — when this machine has no state for the
instance, when the plan would delete or replace anything the configuration
manages, or when it would shorten the event store's recovery window or drop an
analytics reader nobody declared. Volume sizes are kept. NATS servers restart one
at a time under --ha; without it the single server restarts and the broker is
unavailable meanwhile. The upgrade waits for both to be healthy before it moves
the services. Values you set on the instance's OpenTofu configuration are kept
when they are in a terraform.tfvars beside its state
(~/.devicechain/instances/\<instance>/infra/instance/), and not otherwise.
--skip-infrastructure moves only the services, and says what it left.

```
  # Move an instance onto a published release
  dcctl upgrade local devicechain --version v1.3.0

  # Point at images you built yourself
  dcctl upgrade local devicechain --registry localhost:5000 --version my-build

  # See what it would do first, including the broker and event store plan
  dcctl upgrade local devicechain --version v1.3.0 --dry-run

  # Move only the services, leaving the broker and event store as they are
  dcctl upgrade local devicechain --version v1.3.0 --skip-infrastructure

  # Tune the instance; later upgrades keep applying it without --values
  dcctl upgrade local devicechain --values tuning.yaml

  # Once DNS points at the new controller, stop serving the host from ingress-nginx too
  dcctl upgrade local devicechain --drop-legacy-ingress

  # Turn the fail-closed MQTT acknowledgement on (kept by later upgrades), or back off
  dcctl upgrade local devicechain --mqtt-fail-closed
  dcctl upgrade local devicechain --mqtt-fail-closed=false
```

```
dcctl upgrade <provider> <instance> [flags]
```

### Options

```
      --drop-legacy-ingress             stop rendering the Ingress objects beside the Gateway: run it once DNS points at the new controller, and before 'dcctl install --remove-legacy-ingress' removes ingress-nginx, which it refuses to do while any Ingress object still names its class
      --dry-run                         print what would happen without changing anything
      --escrow-file string              where this instance's root-key escrow artifact lives (default ~/.devicechain/escrow/<instance>-rootkey.escrow). The upgrade checks it still protects the key the instance is running on
      --escrow-passphrase-file string   read the escrow passphrase from this file (or $DCCTL_ESCROW_PASSPHRASE). Only needed to WRITE an escrow for an instance that has none — checking an existing one needs no passphrase
  -h, --help                            help for upgrade
      --ingress-controller string       which front end serves the instance's host: gateway (a Gateway API Gateway and HTTPRoutes, served by Traefik), nginx (Ingress objects, served by ingress-nginx) or auto. The choice is RECORDED on the instance and kept by every later upgrade, so 'nginx' stays a rollback until 'gateway' or 'auto' says otherwise; auto drops the record and decides from the cluster. With no choice recorded or given, where Traefik runs the instance is served by the Gateway, and while it still has Ingress objects and ingress-nginx remains they are rendered beside it, so the host keeps answering there until --drop-legacy-ingress. nginx needs ingress-nginx on the cluster, and gateway needs Traefik
      --kube-context string             kube-context to target (default: the cluster recorded at bootstrap; a different context is refused)
      --mqtt-fail-closed                make a QoS 1 PUBACK from the MQTT gateway mean the platform holds the message (infrastructure.nats.mqttFailClosed). The trade: a QoS 1 publish is REFUSED (no PUBACK, connection closed, the device resends) while the broker's MQTT message store has no leader, instead of being acknowledged and possibly lost, and the ingest ceiling is lower, so size the instance below it. It covers QoS 1 only, and its guarantee is stated for a replicated broker (a cluster installed with --ha). --mqtt-fail-closed=false turns it off again. Fixed configuration, recorded on the instance and kept by 'dcctl upgrade' when the flag is omitted; not giving the flag leaves the platform default
      --registry string                 image registry to pull from (default: ghcr.io/devicechain-io)
      --reset-values                    remove the tuning overlay recorded on the instance: it returns to the values dcctl computes. Cannot be combined with --values
      --skip-infrastructure             move only the services: leave this instance's message broker and event store on the configuration they were built with
      --trusted-proxies strings         CIDR ranges of the proxies whose X-Forwarded-For the services believe (infrastructure.http.trustedProxies); repeat or comma-separate. Default: the cluster's pod range, read from its nodes, which is where the ingress controller runs; add a fronting load balancer's ranges when there is one. It is recorded on the instance and kept by every upgrade; "auto" returns to deriving it from the cluster
      --values stringArray              a YAML file of operator tuning (replica counts, resources, an area's own configuration), merged over the values dcctl computes (repeatable; later files win). The overlay is recorded on the instance, so 'dcctl upgrade' keeps applying it; on upgrade, --values REPLACES the recorded overlay. Only tunable values are accepted: a key dcctl owns (images, credentials, endpoints, ingress host and TLS, the area set) is refused, naming it, before anything changes
      --version string                  release version to upgrade to (default: this dcctl's pinned version)
```

### SEE ALSO

* [dcctl](#dcctl)	 - DeviceChain CLI

## dcctl version {#dcctl-version}

Get version info

### Synopsis

Gets build information for the CLI: version, source commit, build date,
and the container image tag this binary deploys by default.

A dcctl built outside the makefile carries no build stamps; the fields it cannot
know are reported as unknown rather than guessed.

```
dcctl version [flags]
```

### Options

```
  -h, --help    help for version
      --short   print just the version string (for scripts)
```

### SEE ALSO

* [dcctl](#dcctl)	 - DeviceChain CLI
