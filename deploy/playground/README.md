# The hosted playground

A DeviceChain instance that anyone can look at without installing anything: a few seeded
blueprint tenants, fed live by simulators, put back the way they started every night.

This directory is the OpenTofu root that stands it up and the notes for the person who
turns it on. **Applying it creates cloud resources that cost money, and putting it on the
internet is the maintainer's decision, so nothing here has been applied to Google Cloud or
exposed.** It was validated on a kind cluster (see [What was validated](#validated)).

It is an OpenTofu root of its own, beside `deploy/gke`, and not under `deploy/opentofu`:
it creates a cluster instead of installing into one, and `dcctl` neither reads it nor ships
it (`hack/tofu-roots.sh` lists what `dcctl` embeds).

## What it is made of

| Piece | Where | Does |
| --- | --- | --- |
| A GKE cluster, one spot node pool | `cluster.tf` (`create_cluster`) | The cheapest cluster the compact install of the default profile fits on. |
| A budget alert | `cluster.tf` | Mails at 50%, 90% and 100% of the monthly budget, and at 100% of forecast. It alerts; Google does not stop spending. |
| The driver | `driver.tf` (`deploy_driver`) | One pod: creates today's seeded tenants with `dcctl sim create` and keeps a `dc-simulator` feeding each. |
| The nightly reset | `driver.tf` | A CronJob that rolls the driver at the start of each playground day. |
| The policy | `backend/tools/playground` | Which tenants exist on which day. Tested; maintainer-only, shipped in no release. |

It creates **no address, no DNS record and no load balancer**.

## How the nightly reset works

A deleted tenant's token is held for at least 12 hours (see the tenant deletion page), so
a reset that deletes the tenants and creates them again under the same names is refused by
the platform. The driver rotates instead.

The playground owns `slots` (default 3) sets of tenants, named `sim-pg-<scenario>-<slot>`.
The playground's *day* begins at `reset_hour_utc`. On a new day the driver creates the new
day's slot from the fixed seed, then destroys every other slot. A visitor's changes live in
a slot that is destroyed the next morning, and the slot that replaces it is made from the
seed, so what a visitor sees at the start of a day is the seed and nothing else. A slot is
destroyed on the day after it was used and not recreated until `slots - 1` days later, so
its token has been free for at least a day.

- The CronJob fires at five past the hour and its only job is to roll the driver
  Deployment. A rolled driver starts fresh and rotates. A driver that merely restarts in the
  middle of a day finds the same day and keeps it.
- The driver also exits at the day boundary by itself, so a missed CronJob is a late reset
  and not a missed one.
- A failed destroy is logged and retried at the next start; a failed create is fatal and the
  pod crash-loops where the maintainer will see it.

## Prerequisites

- A Google Cloud project **used for nothing else**, with billing linked. Do not use the
  project the performance rounds run in: the budget alert only means something if nothing
  else bills there.
- A billing account ID, and an address that should receive the budget alert.
- `tofu` 1.8 or newer, `gcloud`, `kubectl`, `docker`, `gh`, `go`.
- A registry you can push to (the driver image is not published).
- A remote, encrypted, access-controlled OpenTofu state backend. **This root's state holds
  the instance superuser password**; do not leave it in a local file.

## Deploying it {#deploy}

Every step below is the maintainer's. Nothing is automated, because each one either spends
money or makes a decision.

### 1. The cluster

```bash
cd deploy/playground
tofu init
tofu apply \
  -var create_cluster=true \
  -var project_id=<playground project> \
  -var billing_account=<XXXXXX-XXXXXX-XXXXXX> \
  -var budget_alert_email=<you@example.com>
```

Defaults: one `e2-standard-8` spot node in `us-east4-b`, `$300` a month, deletion
protection on. The budget alert is created in the same apply and the plan is refused
without the three inputs above. `tofu output get_credentials_command` prints the command
that adds the cluster to your kubeconfig; `tofu output kube_context` is the context it
creates.

A spot node can be taken back by Google at any time. The instance restarts with it and the
driver recreates the day's tenants on its way back up; a visitor may see an empty page for
a few minutes. That is the price of the pool.

### 2. DeviceChain

Install the release you intend to run, compact, with the default profile:

```bash
dcctl install local --kube-context <context> --compact --no-tls=false
dcctl bootstrap local playground --kube-context <context> --profile default \
  --escrow-passphrase-file <file>
```

`local` is the provider name `dcctl` uses for any cluster named with `--kube-context`, GKE
included. `--no-tls=false` keeps cert-manager, which the public hostname's certificate needs;
see the bootstrap documentation for the ingress and TLS settings. The default profile
leaves out the areas that open an outbound surface (outbound connectors, AI inference) and
the MCP endpoint; keep it that way. Do not add inbound integrations, notification channels
or connectors to the playground's tenants.

### 3. The driver image

```bash
deploy/playground/image/build.sh <version> <registry>/playground-driver:<version> --push
```

It takes `dcctl` and `dc-simulator` from the release of `<version>`, which must be the
version the instance runs, and builds the `playground` binary from this checkout.

### 4. The driver

Read the superuser password from the instance's own Secret, and apply with the in-cluster
half switched on:

```bash
tofu apply \
  -var deploy_driver=true \
  -var kubeconfig_context=<context> \
  -var driver_image=<registry>/playground-driver:<version> \
  -var instance_id=playground \
  -var instance_namespace=dci-playground \
  -var sim_server=<the host the pod reaches the instance at> \
  -var sim_host_header=<hostname> \
  -var 'sim_create_args=["--ingress","http://<ingress>:8081","--mqtt-broker","tcp://<broker>:1883"]' \
  -var superuser_password=<from the instance Secret>
```

`dcctl sim`'s in-cluster defaults are a workstation's, so name the Host the Gateway routes on
(`sim_host_header`) and the device ingress and the MQTT gateway (`sim_create_args`) the way you
would for `dcctl sim create` from outside. They are two variables because `dcctl sim destroy`
takes fewer flags than `create` and refuses the rest.
Check the driver is seeding:

```bash
kubectl -n dc-playground logs deploy/playground-driver
```

Use the first run's log to confirm that the day's tenants were created and the simulators
are running before doing anything else.

### 5. Going public (not done by the playground root)

Deciding what the internet can reach is deliberately outside this root. Before doing it:

- Create the DNS record and the TLS certificate for the instance's hostname yourself.
- Decide what a visitor signs in as. **A read-only visitor identity does not exist yet**:
  the simulators' scoped identities are tenant administrators, and publishing one is
  publishing write access. Do not expose the console until a viewer-role identity is in place.
- Do not expose the broker (port 1883) or the device ingress.
- The read-only MCP endpoint on the playground is not built yet (see
  [What is not here](#not-here)).

### Teardown

```bash
tofu apply -var deploy_driver=false            # removes the driver
tofu apply -var create_cluster=true -var deletion_protection=false ...   # then
tofu destroy -var create_cluster=true ...
```

## Variables worth knowing

| Variable | Default | Notes |
| --- | --- | --- |
| `scenarios` | `sitepulse, buildingpulse, fleetpulse` | Also allowed: `widgetlab`, `devicepulse`. Not `towerpulse`. |
| `slots` | `3` | Never fewer than 3. |
| `reset_hour_utc` | `3` | The driver and the CronJob follow this one number. |
| `spot` | `true` | |
| `budget_usd` | `300` | |
| `network_policy` | `true` | Confines the driver to DNS, the instance and the ingress controller's namespace (`ingress_namespace`). Ignored by a network plugin that does not enforce policies. |

## Checks

```bash
cd deploy/playground
tofu fmt -check -recursive
tofu init -backend=false && tofu validate
tofu test        # mocked providers: no credentials, no cluster
cd ../../backend/tools/playground && go test ./...
```

CI runs the same (`opentofu` job).

## What was validated {#validated}

On a kind cluster, never on Google Cloud: the in-cluster half of the root was applied to a
fresh kind cluster beside a DeviceChain instance, the driver seeded the day's tenants, a
mutation was planted in a live tenant, the reset was run, and the mutation was gone while
the seeded content was identical. The details are in the pull request that added this.

## What is not here {#not-here}

- **The read-only MCP endpoint and the visitor identity.** Both need a decision about how a
  visitor with no account is given a read-only token, which is an authentication decision
  and not a deployment one.
- **A `coldline` tenant.** The simulator has no such scenario yet; the seeded set is the
  scenarios it does have.
- **A byte-for-byte state digest.** The reset creates a tenant from a fixed seed; the check
  that the new tenant equals the seed is the test in the pull request, not a runtime check.
- **Anything public.** See "Going public".
