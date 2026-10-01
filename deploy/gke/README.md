# DeviceChain on Google Kubernetes Engine

This directory creates a GKE cluster sized for DeviceChain. It creates the cluster
and its node pools and nothing inside them. DeviceChain is then installed with
`dcctl`, the same way it goes onto any existing cluster.

It is an OpenTofu (or Terraform) configuration of its own. It keeps its own state,
and `dcctl` neither reads it nor ships it.

## What it creates

| Resource | Default |
| --- | --- |
| A zonal GKE cluster on the `REGULAR` release channel | `devicechain` in `us-east4-b` |
| A `database` node pool, tainted so only pods placed there run there | 3 × `n2-standard-4` (4 vCPU, 16 GB) |
| A `services` node pool for everything else | 3 × `n2-custom-4-8192` (4 vCPU, 8 GB) |
| Every node's boot disk | 100 GB `pd-standard` |
| An optional `loadgen` node pool, tainted so only a load generator runs there | none; set `loadgen_node_count = 1` |

`dcctl install --ha` needs at least three nodes in each pool. NATS puts each of its
three servers on a different `services` node, and the relational store and the
event store, placed on the `database` pool, each put their three instances on
different `database` nodes. See [Install DeviceChain](#install-devicechain).

Check the price of the six nodes and their disks with Google's
[pricing calculator](https://cloud.google.com/products/calculator) before you apply:
a custom machine type's vCPUs and memory are priced separately from the predefined
types'. **It bills until you destroy it.** See [Tearing it down](#tearing-it-down).

## Choosing the machine types

The two pools want different machines. The services are CPU-bound first: in our
runs on 4 GB services nodes the busiest ran at 80 to 93% CPU. Memory came second,
with 1.2 to 2.0 GB left available and pages read back from disk several times a
second; NATS alone used about 0.7 GB per server. The databases want memory:
Postgres keeps a small buffer cache of its own and leans on the node's page cache
for the rest, so memory on a database node buys speed. That is why the `database`
pool has 16 GB nodes and the `services` pool 8 GB ones, with the same 4 vCPU each.
The services shape is a custom one, `n2-custom-4-8192`, because no predefined N2
type has 4 vCPU and 8 GB. For more throughput, `n2-highcpu-8` adds CPU, which is
what binds first. It needs 12 more vCPUs, which takes the cluster past a new
project's 32 (see below).

The busiest pod is the event store's primary together with its write-ahead-log
archiver. When a database pod is scheduled it prefers a node without the other
database's primary, but that is a preference, and a failover, or the switchover
that a node upgrade causes, can put both primaries on one node. In our runs on
8-vCPU nodes the two primaries together used about five cores at 4,000 events a
second, which is more than a 4-vCPU node has. See
[Where the database primaries run](https://docs.devicechain.io/deployment/bootstrap#ha-database-primaries)
for how to check, and how to move one.

We have not measured DeviceChain's throughput on this shape. The published
throughput figures were measured on three 8-vCPU nodes.

The defaults use 24 vCPUs, and 28 with a load-generator node. The extra node GKE
adds to a pool while it upgrades it takes 4 more, which reaches 32. A larger shape
needs a larger `CPUS_ALL_REGIONS` quota and the region's `CPUS` quota (the commands
are in step 4 of [Before you start](#before-you-start)). Changing a pool's machine
type, boot disk type or boot disk size recreates that pool's nodes, which evicts
everything running on them.

## Before you start

You need `gcloud`, `kubectl`, `tofu` (or `terraform`) and `dcctl` on your `PATH`,
and a Google Cloud project with billing linked. The configuration needs OpenTofu
or Terraform 1.8 or newer.

1. Sign in. The first command is for `gcloud` itself, and the second gives
   OpenTofu credentials:

   ```bash
   gcloud auth login
   gcloud auth application-default login
   gcloud config set project <project-id>
   ```

   On WSL, `gcloud` opens a browser inside Linux rather than your Windows one. Use
   `BROWSER=wslview gcloud auth login` (from the `wslu` package), or add
   `--no-launch-browser` and open the printed link yourself.

   OpenTofu signs in with the application-default credentials, not your `gcloud`
   login, and the two can be different accounts. If `apply` fails with
   `Required "container.clusters.create" permission`, run the second command again
   and sign in with the account that owns the project.

2. Install the plugin `kubectl` uses to authenticate to GKE, if you don't have it:

   ```bash
   gcloud components install gke-gcloud-auth-plugin
   # or, where gcloud came from apt: sudo apt install google-cloud-cli-gke-gcloud-auth-plugin
   ```

3. Turn on the APIs:

   ```bash
   gcloud services enable container.googleapis.com compute.googleapis.com
   ```

4. Check your quota. A new project often allows only **32 vCPUs across all
   regions** and **500 GB of SSD per region**. The default cluster uses 24 vCPUs,
   and 28 with a load-generator node.

   **A default `--ha` install with one instance fits a new project's 500 GB of
   SSD.** With one instance, DeviceChain claims 348 GB of persistent volumes,
   160 GB of it the backup store; the
   [prerequisites](https://docs.devicechain.io/deployment/bootstrap#prerequisites)
   list them. Both of GKE's disk classes, `standard-rwo` (balanced) and
   `premium-rwo` (SSD), count against the `SSD_TOTAL_GB` quota. The nodes' boot
   disks do not: they are standard persistent disks, which count against
   `DISKS_TOTAL_GB`, and the default six nodes' boot disks use 600 GB of it. A
   load-generator node and the extra node GKE adds to a pool while it
   [upgrades it](https://cloud.google.com/kubernetes-engine/docs/concepts/node-pool-upgrade-strategies#surge)
   add 100 GB each, and a regional cluster (`location` set to a region) triples
   the boot disks, so check `DISKS_TOTAL_GB` too. A standard disk's speed grows
   with its size, which is why the boot disks are not smaller. We have not
   measured DeviceChain's nodes on standard boot disks, including how long a new
   node takes to pull its images.

   A second instance needs more SSD: each further instance claims 144 GB more,
   and one that ingests continuously also wants about 160 GB more backup store.
   Request an `SSD_TOTAL_GB` quota of at least 700 GB before you install a second
   instance. Without it, a volume that does not fit stays `Pending` with
   `QUOTA_EXCEEDED` in its events.

   Setting a `*_disk_type` to `pd-balanced` or `pd-ssd` puts that pool's boot
   disks back on the SSD quota, 100 GB a node. Set it in `terraform.tfvars`
   before the first `tofu apply`, because changing it later recreates that pool's
   nodes.

   ```bash
   gcloud compute project-info describe --format=json \
     | jq '.quotas[] | select(.metric=="CPUS_ALL_REGIONS")'
   gcloud compute regions describe us-east4 --format=json \
     | jq '.quotas[] | select(.metric=="CPUS" or .metric=="SSD_TOTAL_GB" or .metric=="DISKS_TOTAL_GB")'
   ```

   Ask for more under **IAM & Admin → Quotas**.

## Create the cluster

Zones run out of a machine type more often than you might expect: in our own runs
`us-central1-a`, `us-central1-c` and later `us-east4-a` each had no `n2` capacity
for a while. Probing first (see below) takes a minute; finding out after a failed
apply takes about 45.

```bash
cd deploy/gke
cp terraform.tfvars.example terraform.tfvars   # set project_id
tofu init
tofu apply
```

Creating the cluster takes 10 to 15 minutes. If a node pool sits in `PROVISIONING`
and then fails with `does not have enough resources available`, the zone has run
out of that machine type. Nothing is wrong with your configuration, and a
neighbouring zone in the same region is often out too. Before moving the cluster,
which rebuilds it, check that a zone has capacity by starting one VM of each
type and deleting them:

```bash
gcloud compute instances create cap-probe-db --zone us-east4-b \
  --machine-type n2-standard-4 --image-family debian-12 --image-project debian-cloud \
  --boot-disk-size 10 --no-address
gcloud compute instances create cap-probe-svc --zone us-east4-b \
  --machine-type n2-custom-4-8192 --image-family debian-12 --image-project debian-cloud \
  --boot-disk-size 10 --no-address
gcloud compute instances delete cap-probe-db cap-probe-svc --zone us-east4-b --quiet
```

If both reach `RUNNING`, set `location` to that zone and apply again, which
replaces the cluster.

A node pool that is still trying blocks the cluster: GKE refuses to delete it, and
OpenTofu's refresh waits on the pool, until the pool gives up (about 35 minutes)
and goes to `ERROR`. After that, `tofu apply` with the new `location` replaces the
cluster.

### If an apply fails part-way

A transient error from the Google Cloud API while the cluster is created, such as
a timeout or a server error, can leave OpenTofu marking the cluster *tainted*. The
next plan then says `google_container_cluster.this` "is tainted, so must be
replaced": the apply would destroy and recreate it, which takes another 10 to 15
minutes and loses anything installed on it. If the cluster itself came up
(`gcloud container clusters list` shows it `RUNNING`) and the plan shows the
cluster as the only thing to replace, clear the mark instead:

```bash
tofu untaint google_container_cluster.this
gcloud container node-pools list --cluster devicechain --location us-east4-b
```

Use your own `cluster_name` and `location`. The cluster is created with a
temporary pool called `default-pool`, which the same step deletes once the cluster
exists, so a failure there can leave it behind, and no later apply removes it. It
has no taint, so the services can land on it, and its boot disk draws on the SSD
quota. If the list shows it, delete it:

```bash
gcloud container node-pools delete default-pool --cluster devicechain --location us-east4-b --quiet
```

Then finish the apply:

```bash
tofu plan     # should only create node pools: nothing destroyed or replaced
tofu apply
```

With Terraform, use `terraform untaint` the same way. A tainted node pool, rather
than the cluster, is quicker to let the apply replace.

Once it is up, add it to your kubeconfig. The exact command is in the outputs:

```bash
$(tofu output -raw get_credentials_command)
kubectl config current-context        # gke_<project>_<location>_<cluster>
```

### Changing an existing cluster

A cluster created by an earlier version of this configuration has one `platform`
pool. Applying this version removes that pool and creates the `database` and
`services` pools, in no guaranteed order, so for a while the project may need both
shapes at once: up to 48 vCPUs and the boot disks of nine nodes, with the volumes of
anything still installed on top. That is over a new project's vCPU quota. Recreate the
cluster instead: take it down as [Tearing it down](#tearing-it-down) describes,
then `tofu apply` and install again.

Before that apply, move your settings to the new variable names.
`node_machine_type`, `node_count`, `node_disk_type` and `node_disk_size_gb` are
replaced by `database_*`, `services_*` and `loadgen_*` variables for each pool, and
OpenTofu only warns about an old name left in `terraform.tfvars` and ignores its
value, so a `node_machine_type` or `node_disk_type` choice would be dropped and the
pools built with the defaults.

### Put the databases on SSD

GKE's default StorageClass, `standard-rwo`, is a balanced persistent disk. Postgres
and NATS commit every write to disk before they acknowledge it, so how fast the
disk syncs sets how fast the platform can go. Make the SSD class the default
**before** installing, because a volume keeps the class it was created with:

```bash
kubectl patch storageclass standard-rwo \
  -p '{"metadata":{"annotations":{"storageclass.kubernetes.io/is-default-class":"false"}}}'
kubectl patch storageclass premium-rwo \
  -p '{"metadata":{"annotations":{"storageclass.kubernetes.io/is-default-class":"true"}}}'
kubectl get storageclass                 # premium-rwo should be marked (default)
```

### Volume-snapshot base backups (optional)

By default each database's daily base backup is a full copy in the backup store.
`--backup-snapshot-class` takes it as a persistent-disk snapshot instead, which GKE
takes incrementally and keeps as a project resource; a full copy still goes to the
backup store weekly, and every restore still reads the backup store. See
[Volume-snapshot base backups](https://docs.devicechain.io/deployment/bootstrap#snapshot-base-backups)
for what it trades.

GKE's persistent-disk driver (`pd.csi.storage.gke.io`) provisions both `premium-rwo`
and `standard-rwo`, and GKE runs the snapshot controller. Look for a class for that
driver; if there is none, create one. It must delete the disk snapshot when its
VolumeSnapshot is deleted, or pruning frees nothing:

```bash
kubectl get volumesnapshotclass
kubectl apply -f - <<'EOF'
apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshotClass
metadata:
  name: pd-snapshots
driver: pd.csi.storage.gke.io
deletionPolicy: Delete
EOF
```

Then add `--backup-snapshot-class pd-snapshots` to the install command below.
`dcctl install` refuses a class that does not exist, keeps its snapshots
(`deletionPolicy: Retain`), or belongs to another driver.

To see them:

```bash
kubectl get scheduledbackup -A           # dc-rdb-snapshot, dc-tsdb-snapshot: volumeSnapshot
kubectl get volumesnapshot -A
gcloud compute snapshots list
```

The DeviceChain operator deletes snapshots outside each database's recovery window,
keeping the newest one before it. Deleting the GKE cluster without first destroying the instances leaves their
disk snapshots in the project, holding the databases' contents: delete them with
`gcloud compute snapshots delete`.

To check pruning is running, look for a recent pass time on each snapshot schedule.
The operator makes a pass every ten minutes:

```bash
kubectl get scheduledbackup -A -l app.kubernetes.io/component=database-snapshot-backup \
  -o custom-columns='NAMESPACE:.metadata.namespace,NAME:.metadata.name,CHECKED:.metadata.annotations.devicechain\.io/snapshot-retention-checked-at'
```

An empty `CHECKED` column more than ten minutes after a schedule appears means the
operator is not pruning that schedule, and `DatabaseSnapshotPruningStalled` fires
within about half an hour after that. (An instance's `dc-tsdb-snapshot` appears when
the instance is bootstrapped, not at install.)

## Install DeviceChain

The `database` pool carries GKE's own pool label,
`cloud.google.com/gke-nodepool=database`, and the taint
`dedicated=database:NoSchedule`, so only a pod that tolerates the taint runs there.
`--database-node-selector` and `--database-toleration` put the databases there:
the relational store now, and the event store of every instance you bootstrap later.
`tofu output database_node_selector` and `tofu output database_taint` print the two
values. Add any other install flags, such as `--backup-snapshot-class`, to this one
command, and set the placement at the first install: changing it later is refused
while any instance runs. See
[Database placement](https://docs.devicechain.io/deployment/bootstrap#database-placement).

```bash
CTX=$(tofu output -raw kube_context)

dcctl install local --kube-context "$CTX" --ha \
  --database-node-selector "$(tofu output -raw database_node_selector)" \
  --database-toleration "$(tofu output -raw database_taint)"
```

`dcctl bootstrap` needs nothing extra: every instance's event store follows the
install. NATS and the services carry no toleration, so they run on the `services`
pool. To check where the databases landed:

```bash
kubectl --context "$CTX" get pods -A -l cnpg.io/cluster -o wide
```

The ingress controller gets a Google Cloud load balancer with a public IP. Wait for
the address, then build an instance whose host name points at it:

```bash
kubectl --context "$CTX" get svc -A | grep ingress-nginx-controller   # EXTERNAL-IP
IP=<external-ip>

dcctl bootstrap local my-instance --kube-context "$CTX" --host "$IP.nip.io"
```

`nip.io` resolves any `<ip>.nip.io` name to that IP, so you don't need a DNS record
for a trial. The certificate is self-signed unless you configure your own. See
[Bootstrap](https://docs.devicechain.io/deployment/bootstrap) for every install and
bootstrap option, and keep the root-key escrow file it writes.

### Deploying images you built

`dcctl` deploys published release images by default. To run a build of your own,
push the images to a registry the nodes can pull from (an Artifact Registry
repository in the same project needs no extra permissions) and name it on both
commands:

```bash
gcloud artifacts repositories create dc --repository-format=docker --location=us-east4
gcloud auth configure-docker us-east4-docker.pkg.dev
REGISTRY=us-east4-docker.pkg.dev/<project-id>/dc TAG=<tag> deploy/local/build-images.sh

dcctl install local --kube-context "$CTX" --ha --registry us-east4-docker.pkg.dev/<project-id>/dc --version <tag>
dcctl bootstrap local my-instance --kube-context "$CTX" --host "$IP.nip.io" \
  --registry us-east4-docker.pkg.dev/<project-id>/dc --version <tag>
```

`dcctl` refuses a version containing `-dev.`, because it treats that as a build
nobody published. Tag your images with something else, such as `v0.18.1-bench.<sha>`.

### Long sessions

`gcloud` and the application-default credentials can stop working mid-session if
your organization requires periodic re-authentication. Every `kubectl` and
`dcctl` command against the cluster then fails until you run `gcloud auth login`
and `gcloud auth application-default login` again. Don't leave a cluster running
unattended past that point: nothing can tear it down until someone signs in.

## Restoring an instance's event store

No restore reads a snapshot; a restore reads the backup store. To rebuild one
instance's event store from it, for example to a moment before a mistaken delete,
first read the path its event store archives under, while the instance still exists:

```bash
kubectl --context "$CTX" -n dci-my-instance get cluster dc-tsdb \
  -o jsonpath='{.spec.plugins[*].parameters.serverName}'
```

**This rebuilds the instance with an empty control plane.** The destroy also drops
the instance's database on the shared relational store, so its tenants, devices,
users and stored secrets are deleted and are not restored; only its event history
comes back. To bring both back, rebuild the cluster and recover both databases
instead, following [the full recovery procedure](https://docs.devicechain.io/deployment/disaster-recovery#recover).

Move the instance's escrow artifact aside, because bootstrap will not overwrite it,
and keep it: it is still the only key to the relational backups taken before the
destroy. Then destroy the instance, keeping its backups, and bootstrap it again from
the archive with the same options you built it with:

```bash
mv ~/.devicechain/escrow/my-instance-rootkey.escrow ~/my-instance-rootkey.before-restore.escrow

dcctl destroy local my-instance --kube-context "$CTX" --keep-backups --yes
dcctl bootstrap local my-instance --kube-context "$CTX" --host "$IP.nip.io" \
  --restore-tsdb-from <archive-path> --restore-tsdb-at <RFC3339 time before the damage>
```

Leave out `--restore-tsdb-at` to replay the whole archive, and add `--registry` and
`--version` if you built the instance from your own images. Do not pass
`--restore-root-key`: bootstrap refuses it here, because the destroy removed the
instance's data from the relational store and there is nothing for that key to
open; the rebuilt instance mints a new one. `--keep-backups` keeps the backup
store's archive, which is what the restore reads; the instance's VolumeSnapshots and
their disk snapshots are deleted with its namespace either way.

## Tearing it down

Destroying the cluster does **not** delete everything it caused to be created.
Persistent volumes and the load balancer belong to your project, not to the cluster,
so remove what created them while the cluster still exists.

`dcctl destroy` removes an instance, including its volumes. There is no command yet
that removes what `dcctl install` put on the cluster, and that includes the
relational store's volumes (`dc-system`), the monitoring stack's volume and the
ingress load balancer. Deleting those namespaces releases them:

```bash
dcctl destroy local my-instance --kube-context "$CTX"
kubectl --context "$CTX" delete namespace ingress-nginx dc-system monitoring
tofu destroy
```

Then check that nothing is left billing:

```bash
gcloud compute disks list --filter="name~^pvc-"
gcloud compute forwarding-rules list
gcloud compute addresses list
```

All three should come back empty. Delete anything they list with the matching
`gcloud compute … delete` command.
