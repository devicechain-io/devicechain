# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# The Traefik Gateway API controller (G15): the L7 entry point that replaces
# ingress-nginx. OpenTofu installs the controller (the capability); the per-instance
# Gateway and HTTPRoutes that route to the app Services are rendered by the Helm chart
# (deploy/helm/devicechain), which knows the enabled functional areas.
#
# This release runs it NEXT TO ingress-nginx. Nothing here touches that controller, and
# nothing here makes this one the cluster's default: its IngressClass is not the default
# class and it only serves Ingresses that name it.
#
# 🔴 EVERY SETTING BELOW THAT LOOKS LIKE TUNING IS A MEASURED FACT ABOUT THIS CONTROLLER,
# and each is pinned by tests/ because the chart does not fail on a wrong one. A key it
# does not know is ignored and the install comes up healthy without the behaviour
# (`service.type` instead of `service.spec.type` is the live example). So the timeouts,
# the query-string drop, the trusted-proxy list and the Service shape are read back out of
# the values this module hands the chart, not just trusted to be written.

# Named so hack/check-tofu-module-tests.sh pins the provider version the shipped roots run (it only pins
# a provider the module declares), instead of testing against whatever the registry calls latest.
terraform {
  required_providers {
    helm = {
      source = "hashicorp/helm"
    }
  }
}

variable "namespace" {
  type    = string
  default = "traefik"
}

variable "release_name" {
  type    = string
  default = "traefik"
}

variable "chart_version" {
  description = <<-EOT
    Traefik chart version. PINNED, and the pin is not hygiene: the keys this module
    sets are chart keys, and a chart that renames one does not fail the install, it
    silently drops the behaviour (an entrypoint timeout, the query-string drop, the
    trusted-proxy list). 42.0.0 ships Traefik v3.7.14, where this recipe was verified
    on a kind cluster. Keep image_tag in step with the chart's appVersion.

    Bumping is a deliberate act: render the new chart with this module's values
    (helm template traefik/traefik --version <pin> -f <values>), read the container args
    and the Service back, run the module's tests, and apply one instance and confirm a
    Gateway is Accepted and Programmed. Keep the root's traefik_chart_version in step
    (hack/check-chart-pins.sh enforces that they agree).
  EOT
  type        = string
  default     = "42.0.0"

  # Fail closed. An empty string used to mean "install latest", which made the chart
  # version resolve at APPLY time -- see the ingress-nginx module for the failure that
  # produces. The regex also refuses a helm version RANGE, which is a constraint resolved
  # at apply time wearing a pin's clothes.
  validation {
    condition     = can(regex("^v?[0-9]+\\.[0-9]+\\.[0-9]+(-[0-9A-Za-z.]+)?$", var.chart_version))
    error_message = "chart_version must be an exact chart version (e.g. \"1.2.3\" or \"v1.2.3\"); an empty value or a version range is resolved at apply time, which is not a pin."
  }
}

variable "image_registry" {
  description = "Registry the Traefik image is pulled from. Set it (with image_repository) to pull from a mirror."
  type        = string
  default     = "docker.io"
}

variable "image_repository" {
  description = "Repository the Traefik image is pulled from, without the registry."
  type        = string
  default     = "traefik"
}

variable "image_tag" {
  description = <<-EOT
    Traefik image tag, stated explicitly rather than inherited from the chart's
    appVersion so the version that runs is written in this repository. v3.7.14 is the
    release the pinned chart ships. It carries the SNI-check fix for requests whose
    :scheme is http over HTTP/2 and the Gateway allow-list option. v3.7.15 and v3.7.16
    were not taken: their advisories are in the ingress-nginx compatibility provider, the
    experimental FastProxy and ForwardAuth with NTLM/Kerberos, none of which this module
    enables.
  EOT
  type        = string
  default     = "v3.7.14"

  validation {
    condition     = can(regex("^v[0-9]+\\.[0-9]+\\.[0-9]+$", var.image_tag))
    error_message = "image_tag must be an exact release tag such as v3.7.14; a floating tag (latest, v3, v3.7) is a pointer somebody else owns."
  }
}

variable "image_digest" {
  description = <<-EOT
    Digest the image is pulled by (sha256:...). It takes precedence over the tag at pull
    time; the tag stays as the readable version. The default is the multi-arch index of
    traefik:v3.7.14@sha256:575fa15b135078fe5e50aa847987d96dbddd7b093c172429618404df73f3fa7c,
    written out whole so hack/check-image-pulls.sh probes it weekly and a withdrawn image
    is noticed before an install is.

    🔴 IT MUST MOVE WITH image_tag. A digest left behind by a tag bump pulls the OLD image
    while every version string says the new one; tests/chart_values.tftest.hcl pins the pair
    so a bump has to touch both. Set it to "" to pull by tag alone. A mirror that has the
    image by digest serves it unchanged.
  EOT
  type        = string
  default     = "sha256:575fa15b135078fe5e50aa847987d96dbddd7b093c172429618404df73f3fa7c"

  validation {
    condition     = var.image_digest == "" || can(regex("^sha256:[a-f0-9]{64}$", var.image_digest))
    error_message = "image_digest must be empty or sha256:<64 hex digits>."
  }
}

variable "ingress_class" {
  description = "IngressClass name the controller registers and the only class of Ingress it serves. Never the cluster default: another controller's Ingresses must stay that controller's."
  type        = string
  default     = "traefik"
}

variable "gateway_class" {
  description = "GatewayClass name the controller registers (referenced by every instance Gateway)."
  type        = string
  default     = "traefik"
}

variable "use_host_port" {
  description = <<-EOT
    Bind the controller to the node's host 80/443 (hostPort) behind a NodePort Service.
    This is the local-kind recipe, and it can only be on for ONE controller per node:
    ingress-nginx's host-port mode binds the same ports, and the second pod stays
    Pending. So while both controllers are installed, leave this off and reach Traefik
    through its NodePort; turning it on is the swap step. Leave false for real clouds.
  EOT
  type        = bool
  default     = false
}

variable "service_type" {
  description = <<-EOT
    Service type for the controller. LoadBalancer for a cloud. NodePort on kind (or any
    cluster with no load-balancer provider): a LoadBalancer Service stays <pending> there
    and `helm --wait` hangs until its timeout and marks the release failed.
  EOT
  type        = string
  default     = "LoadBalancer"

  validation {
    condition     = contains(["LoadBalancer", "NodePort", "ClusterIP"], var.service_type)
    error_message = "service_type must be LoadBalancer, NodePort or ClusterIP."
  }
}

variable "aws_nlb" {
  description = <<-EOT
    EKS: annotate the controller's LoadBalancer Service for an AWS Network Load Balancer
    (service.beta.kubernetes.io/aws-load-balancer-type: nlb). Off by default. Only a Network
    Load Balancer passes the client's address through, which externalTrafficPolicy: Local
    relies on: a LoadBalancer Service with no annotation gets a Classic balancer, which
    proxies TCP, so the controller sees the balancer's own address and every device shares
    one rate-limit allowance. SWITCHING AN EXISTING SERVICE REPLACES ITS LOAD BALANCER, and
    with it the hostname clients resolve: repoint DNS afterwards. Has no effect unless the
    Service is a LoadBalancer. (PROXY protocol is the other way to keep the address; this
    module does not configure it.)
  EOT
  type        = bool
  default     = false
}

variable "replicas" {
  description = <<-EOT
    Controller replicas. Forced to 1 with use_host_port: two pods cannot share a node's host
    ports. On a cloud LoadBalancer the Service keeps the client address
    (externalTrafficPolicy: Local), so the balancer sends traffic ONLY to nodes that run a
    controller pod: with one replica a rolling update or a node loss leaves no target until
    the pod is back. Two or more are spread over nodes by a preferred anti-affinity and
    protected by a PodDisruptionBudget (the chart renders none of its own).
  EOT
  type        = number
  default     = 1

  validation {
    condition     = var.replicas >= 1 && var.replicas == floor(var.replicas)
    error_message = "replicas must be a whole number of at least 1."
  }
}

variable "drain_seconds" {
  description = <<-EOT
    How long a controller pod keeps serving after it is told to terminate, so a cloud load
    balancer that still points at its node does not hit a closed port. Applied ONLY where
    externalTrafficPolicy is Local (a LoadBalancer Service, no host port): there the balancer
    sends to a node until its health check fails it, which takes about three 8-second probes
    on GCP, so a pod that leaves at once drops requests (connection refused, then connect
    timeouts) until the balancer notices. The pod sleeps this long in preStop, with Kubernetes'
    terminating-endpoint handling keeping the node healthy-and-serving meanwhile. Not applied
    on kind (NodePort / host port): there is no balancer to wait for and the sleep would only
    slow every rollout. 0 turns it off.
  EOT
  type        = number
  default     = 40

  validation {
    condition     = var.drain_seconds >= 0 && var.drain_seconds <= 120 && var.drain_seconds == floor(var.drain_seconds)
    error_message = "drain_seconds must be a whole number between 0 and 120."
  }
}

variable "min_ready_seconds" {
  description = <<-EOT
    Seconds a new controller pod must be ready before the rollout counts it available and
    removes an old one. Applied only where externalTrafficPolicy is Local, for the same reason
    as drain_seconds: the balancer needs time to see the new pod's node as healthy before the
    old pod's node can leave. 0 turns it off.
  EOT
  type        = number
  default     = 15

  validation {
    condition     = var.min_ready_seconds >= 0 && var.min_ready_seconds <= 120 && var.min_ready_seconds == floor(var.min_ready_seconds)
    error_message = "min_ready_seconds must be a whole number between 0 and 120."
  }
}

variable "trusted_proxy_cidrs" {
  description = <<-EOT
    Source ranges whose X-Forwarded-* headers the controller trusts and appends to.
    Empty (the default) is the EDGE posture: the controller is the first hop, every
    incoming X-Forwarded-* and X-Real-Ip is discarded and X-Forwarded-For is reset to
    the real peer, which is what core's right-most-untrusted rule needs. On a cloud
    load balancer that preserves the client address this is paired with
    externalTrafficPolicy: Local, which this module sets.

    Set it when the controller sits BEHIND an L7 load balancer or proxy that you operate
    (Google Cloud HTTP(S) LB, an operator's proxy): list that proxy's ranges here, and
    then core's infrastructure.http.trustedProxies MUST list the pod CIDR AND those same
    ranges. Core's list has to name every trusted hop, not only this controller: the
    controller appends the proxy's address to the chain, and if only the pod CIDR is
    trusted the walk stops at the proxy and every client collapses into one rate-limit
    key.
  EOT
  type        = list(string)
  default     = []

  validation {
    condition     = alltrue([for c in var.trusted_proxy_cidrs : can(cidrhost(c, 0))])
    error_message = "trusted_proxy_cidrs must be CIDR ranges such as 10.0.0.0/8."
  }
}

variable "max_artifact_bytes" {
  description = <<-EOT
    The largest single OTA artifact any instance on this cluster will accept
    (update-management's updates.maxArtifactBytes; its default is 512 MiB). The edge read
    timeout is derived from it, so an upload of that size can finish: Traefik's
    readTimeout bounds the WHOLE request, body included, and its 60s default cuts a large
    upload at exactly 60 seconds. Raise it before raising any instance's limit.
  EOT
  type        = number
  default     = 536870912

  validation {
    condition     = var.max_artifact_bytes > 0 && var.max_artifact_bytes == floor(var.max_artifact_bytes)
    error_message = "max_artifact_bytes must be a positive whole number of bytes."
  }
}

variable "access_log_enabled" {
  description = "Turn the access log on. Off by default; whichever way it is set, query strings are dropped from it (see local.access_log), so the OTA download capability in ?cap= never reaches a log."
  type        = bool
  default     = false
}

variable "enable_ingress_provider" {
  description = "Also serve Ingress resources of ingress_class (Grafana's /grafana route, and the legacy instance Ingress during a migration). The Gateway provider is always on."
  type        = bool
  default     = true
}

variable "gateway_allow_list" {
  description = <<-EOT
    Restrict the Gateways the controller accepts to this list of {name, namespace}.
    Empty (the default) accepts any Gateway of gateway_class. Hostname ownership is NOT
    enforced across Gateways: a second Gateway declaring another instance's hostname is
    Accepted, and its more specific routes win. That needs Gateway write access in some
    namespace, the same exposure class as an Ingress today, and it is closed by RBAC. This
    variable is the opt-in belt for operators who want it closed in the controller too;
    when set it must name EVERY instance Gateway, and a change rolls the controller.
  EOT
  type = list(object({
    name      = string
    namespace = string
  }))
  default = []
}

locals {
  # --- the edge timeouts -------------------------------------------------------------
  #
  # readTimeout is the one that needs working out. Traefik's bounds the whole request,
  # body included, and has no header-only variant, so the same number is both what lets a
  # legitimate OTA upload finish and how long a client dribbling headers can hold a
  # connection. 60s cuts uploads; 0 is unbounded. It is therefore the service's own upload
  # window plus a grace.
  #
  # 🔴 THESE THREE NUMBERS ARE update-management's, restated in the only language this file
  # speaks. UploadWindow(size) in services/update-management/model/api.go is
  # 2 minutes + floor(size / 256 KiB/s) seconds, and claimGrace there is one minute. A Go
  # test in that package reads them back out of THIS file and fails if they move apart, and
  # the module's own tests pin the arithmetic, so neither side can change alone.
  upload_fixed_allowance_seconds = 120
  upload_min_throughput_bytes    = 262144
  upload_claim_grace_seconds     = 60

  upload_window_seconds = local.upload_fixed_allowance_seconds + floor(var.max_artifact_bytes / local.upload_min_throughput_bytes)
  read_timeout          = "${local.upload_window_seconds + local.upload_claim_grace_seconds}s"

  # Downloads and WebSockets stream for as long as the service lets them (it arms its own
  # per-chunk write deadlines), so the controller must not cut them.
  write_timeout = "0"
  idle_timeout  = "180s"

  # The GraphQL edge. HTTPRoute timeouts are not implemented by Traefik (its own
  # conformance report lists HTTPRouteRequestTimeout and HTTPRouteBackendTimeout as
  # unsupported), so the 60s every synchronous browser request lives inside is controller
  # configuration, not chart. Measured: /slow?d=70s returned 504 at 60.02s. The timer starts
  # after the request body is written, so it never touches an upload or a WebSocket.
  #
  # 🔴 THIS IS core/config.RequestEdgeTimeout, restated. A Go test in core/config reads this
  # local and fails if the two differ.
  response_header_timeout_seconds = 60

  # --- the access log ----------------------------------------------------------------
  #
  # The OTA download capability travels in ?cap=, so a log line carrying the query string
  # is a leaked capability. `queryParameters.defaultMode: drop` removes every query
  # parameter from the logged path (v3.7.3+). It is stated whether or not the log is on:
  # the chart only renders the argument while the log is enabled, so the day someone turns
  # it on the safe form is already what they get.
  access_log = {
    enabled = var.access_log_enabled
    fields = {
      queryParameters = {
        defaultMode = "drop"
      }
    }
  }

  # --- the entrypoints ---------------------------------------------------------------
  #
  # Listeners on a Gateway are matched to the controller's ENTRYPOINT port, not to the
  # Service port, so the entrypoints are 80 and 443 and the instance chart can render the
  # standard listener ports. The chart's own 8000/8443 would put Traefik-specific ports in
  # every instance. The bind works as the chart's non-root user (uid 65532, all capabilities
  # dropped) because the pod's network namespace has net.ipv4.ip_unprivileged_port_start=0;
  # that is containerd 2.x behaviour, measured on kind and still to be confirmed on GKE.
  entrypoint = {
    http = {
      # Headers whose names differ only by an underscore/dash alias (X_Forwarded_For vs
      # X-Forwarded-For) are DELETED before routing, so a client cannot smuggle a trusted
      # header past the sanitising of the real one.
      aliasHeadersStrategy = "delete"
    }
    forwardedHeaders = {
      trustedIPs = var.trusted_proxy_cidrs
      insecure   = false
    }
    transport = {
      respondingTimeouts = {
        readTimeout  = local.read_timeout
        writeTimeout = local.write_timeout
        idleTimeout  = local.idle_timeout
      }
    }
  }

  replicas = var.use_host_port ? 1 : var.replicas

  # The chart's own labels, which the anti-affinity selects on: the instance label is
  # "<release>-<namespace>" (read off the rendered chart).
  pod_labels = {
    "app.kubernetes.io/name"     = "traefik"
    "app.kubernetes.io/instance" = "${var.release_name}-${var.namespace}"
  }

  # More than one replica: spread them over nodes (Local sends traffic only to nodes running
  # a pod, so two on one node are one target; preferred, not required, so a one-node cluster
  # still schedules both) and keep one up through a voluntary disruption (a node drain). The
  # chart renders a PodDisruptionBudget only when asked, and not by default.
  spread = {
    for k, v in {
      affinity = {
        podAntiAffinity = {
          preferredDuringSchedulingIgnoredDuringExecution = [{
            weight = 100
            podAffinityTerm = {
              topologyKey   = "kubernetes.io/hostname"
              labelSelector = { matchLabels = local.pod_labels }
            }
          }]
        }
      }
      podDisruptionBudget = { enabled = true, minAvailable = 1 }
    } : k => v if local.replicas > 1
  }

  image = merge(
    {
      registry   = var.image_registry
      repository = var.image_repository
      tag        = var.image_tag
    },
    { for k, v in { digest = var.image_digest } : k => v if var.image_digest != "" },
  )

  # With a digest the pod's image reference carries no tag, so the chart is told which
  # version it is running (it cannot derive one from a digest) for its version-gated flags.
  version_override = var.image_digest == "" ? "" : var.image_tag

  # Hitless rollouts behind a cloud balancer. Only with externalTrafficPolicy: Local (the same
  # condition the Service below uses): the balancer keeps sending to a node that lost its
  # endpoint until its health check fails, so the old pod must outlive that (preStop sleep,
  # native action: the image needs no shell) and the new one must be healthy first
  # (minReadySeconds). terminationGracePeriodSeconds keeps the chart's 60s of headroom on top
  # of the sleep for in-flight requests. Elsewhere the chart's defaults stand.
  etp_local = !var.use_host_port && var.service_type == "LoadBalancer"
  drain = {
    for k, v in {
      minReadySeconds               = var.min_ready_seconds
      lifecycle                     = { preStop = { sleep = { seconds = var.drain_seconds } } }
      terminationGracePeriodSeconds = var.drain_seconds + 60
    } : k => v if local.etp_local && (k == "minReadySeconds" ? var.min_ready_seconds > 0 : var.drain_seconds > 0)
  }

  values = merge(
    {
      image  = local.image
      global = { checkNewVersion = false, sendAnonymousUsage = false }

      versionOverride = local.version_override

      deployment = merge(
        { replicas = local.replicas },
        local.drain,
      )

      # The dashboard is not routed anywhere and exposing the API adds nothing to a
      # controller whose configuration lives in Gateways.
      api = { dashboard = false }

      # The IngressClass is registered but never the default: a default class would hijack
      # every class-less Ingress on the cluster, including ones another controller serves.
      ingressClass = {
        enabled        = true
        isDefaultClass = false
        name           = var.ingress_class
      }

      providers = {
        kubernetesGateway = merge(
          { enabled = true },
          { for k, v in { gateways = var.gateway_allow_list } : k => v if length(var.gateway_allow_list) > 0 },
        )
        kubernetesIngress = {
          enabled      = var.enable_ingress_provider
          ingressClass = var.ingress_class
        }
        # No Middleware / IngressRoute surface. Routing is Gateway API only, so a tenant
        # with namespace-level rights cannot attach controller-specific behaviour.
        kubernetesCRD = { enabled = false }
      }

      # Instances render their own Gateways; the chart's default one would sit on the
      # chart's own ports and claim a listener nobody asked for.
      gateway = { enabled = false }
      gatewayClass = {
        enabled = true
        name    = var.gateway_class
      }

      ports = {
        web = merge(
          {
            port             = 80
            http             = local.entrypoint.http
            forwardedHeaders = local.entrypoint.forwardedHeaders
            transport        = local.entrypoint.transport
          },
          { for k, v in { hostPort = 80 } : k => v if var.use_host_port },
        )
        websecure = merge(
          {
            port             = 443
            http             = local.entrypoint.http
            forwardedHeaders = local.entrypoint.forwardedHeaders
            transport        = local.entrypoint.transport
          },
          { for k, v in { hostPort = 443 } : k => v if var.use_host_port },
        )
      }

      accessLog = local.access_log

      # The key is service.spec.type. `service.type` is silently ignored by the chart's
      # schema, which is why this is read back by a test.
      service = merge(
        {
          spec = merge(
            { type = var.use_host_port ? "NodePort" : var.service_type },
            # Keep the client's address: with the default (Cluster) the peer Traefik sees is a
            # SNAT'd node IP and every client looks like one.
            { for k, v in { externalTrafficPolicy = "Local" } : k => v if !var.use_host_port && var.service_type == "LoadBalancer" },
          )
        },
        # Local keeps the address only behind a balancer that passes the connection through; an
        # unannotated Service gets AWS's Classic one, which proxies TCP. See var.aws_nlb.
        { for k, v in { annotations = { "service.beta.kubernetes.io/aws-load-balancer-type" = "nlb" } } : k => v if var.aws_nlb && !var.use_host_port && var.service_type == "LoadBalancer" },
      )

      additionalArguments = [
        "--serversTransport.forwardingTimeouts.responseHeaderTimeout=${local.response_header_timeout_seconds}s",
      ]

      resources = {
        requests = { cpu = "50m", memory = "64Mi" }
      }
    },
    local.spread,
    # The chart's default update strategy is maxUnavailable 0 / maxSurge 1. With a hostPort
    # on a single node the surge pod can never schedule, so EVERY `helm upgrade` deadlocks:
    # the new pod stays Pending on the port and the old one is never removed.
    {
      for k, v in {
        updateStrategy = { type = "RollingUpdate", rollingUpdate = { maxUnavailable = 1, maxSurge = 0 } }
        nodeSelector   = { "ingress-ready" = "true" }
        tolerations = [{
          key      = "node-role.kubernetes.io/control-plane"
          operator = "Exists"
          effect   = "NoSchedule"
        }]
      } : k => v if var.use_host_port
    },
  )
}

resource "helm_release" "traefik" {
  max_history      = local.helm_max_history
  name             = var.release_name
  namespace        = var.namespace
  create_namespace = true
  repository       = "https://traefik.github.io/charts"
  chart            = "traefik"
  version          = var.chart_version

  values = [yamlencode(local.values)]
}

output "ingress_class" {
  description = "IngressClass name the controller serves."
  value       = var.ingress_class
}

output "gateway_class" {
  description = "GatewayClass name instance Gateways reference."
  value       = var.gateway_class
}

output "namespace" {
  value = var.namespace
}

output "replicas" {
  description = "The controller replicas in force (1 with use_host_port, whatever replicas says)."
  value       = local.replicas
}

output "read_timeout" {
  description = "The edge read timeout in force, derived from max_artifact_bytes."
  value       = local.read_timeout
}
