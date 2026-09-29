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
| A `platform` node pool for DeviceChain | 3 × `n2-standard-8` (8 vCPU, 32 GB) |
| An optional `loadgen` node pool, tainted so only a load generator runs there | none; set `loadgen_node_count = 1` |

Three nodes is the smallest cluster `dcctl install --ha` accepts: the relational
store, NATS and the event store each put one replica on every node.

At on-demand prices the default cluster costs roughly $1.20 an hour, plus its disks.
**It bills until you destroy it.** See [Tearing it down](#tearing-it-down).

## Before you start

You need `gcloud`, `kubectl`, `tofu` (or `terraform`) and `dcctl` on your `PATH`,
and a Google Cloud project with billing linked.

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

   ```bash
   gcloud compute project-info describe --format=json \
     | jq '.quotas[] | select(.metric=="CPUS_ALL_REGIONS")'
   gcloud compute regions describe us-east4 --format=json \
     | jq '.quotas[] | select(.metric=="CPUS" or .metric=="SSD_TOTAL_GB")'
   ```

   Ask for more under **IAM & Admin → Quotas** if you need it.

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
which rebuilds it, check that a zone has capacity by starting one VM of the same
type and deleting it:

```bash
gcloud compute instances create cap-probe --zone us-east4-b \
  --machine-type n2-standard-8 --image-family debian-12 --image-project debian-cloud \
  --boot-disk-size 10 --no-address
gcloud compute instances delete cap-probe --zone us-east4-b --quiet
```

If it reaches `RUNNING`, set `location` to that zone and apply again, which
replaces the cluster.

A node pool that is still trying blocks the cluster: GKE refuses to delete it, and
OpenTofu's refresh waits on the pool, until the pool gives up (about 35 minutes)
and goes to `ERROR`. After that, `tofu apply` with the new `location` replaces the
cluster.

Once it is up, add it to your kubeconfig. The exact command is in the outputs:

```bash
$(tofu output -raw get_credentials_command)
kubectl config current-context        # gke_<project>_<location>_<cluster>
```

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

## Install DeviceChain

```bash
CTX=$(tofu output -raw kube_context)

dcctl install local --kube-context "$CTX" --ha
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
