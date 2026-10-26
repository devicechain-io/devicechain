# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# The NGINX ingress controller (ADR-002): the L7 entry point that fronts the
# DeviceChain GraphQL/HTTP surface. OpenTofu installs the controller (the
# capability); the per-instance Ingress *resource* that routes to the app
# Services is rendered by the Helm chart (deploy/helm/devicechain), which knows
# the enabled functional areas.

terraform {
  required_providers {
    helm = {
      source = "hashicorp/helm"
    }
  }
}

variable "namespace" {
  type    = string
  default = "ingress-nginx"
}

variable "release_name" {
  type    = string
  default = "ingress-nginx"
}

variable "chart_version" {
  description = <<-EOT
    ingress-nginx chart version. PINNED, and the pin is not hygiene — the values
    below re-enable configuration snippets and lower the annotations risk level,
    a surface upstream has tightened repeatedly. 4.15.1 ships controller 1.15.1,
    which is where this recipe was verified: the instance chart's Ingress admits.
    When that gating moves again the failure does not look like a version problem:
    the Ingress is rejected by the admission webhook midway through bootstrap,
    with the controller reporting a snippet or risk-level error.

    Bumping is a deliberate act: install the new chart, then apply an instance and
    confirm its Ingress ADMITS. Keep the root's ingress_nginx_chart_version in step
    (hack/check-chart-pins.sh enforces that they agree — the root's value is the one
    that installs, so a lone module pin documents a chart nobody deploys).
  EOT
  type        = string
  default     = "4.15.1"

  # Fail closed. An empty string used to mean "install latest", which made the chart
  # version resolve at APPLY time — the chart repository became a dependency of
  # planning, and a repo hiccup surfaced as the helm provider's "inconsistent final
  # plan ... .version: was known, but now unknown", naming neither the chart nor the
  # network. The regex also refuses a helm version RANGE ("4.15.1 - 5.0.0"), which is
  # a constraint resolved at apply time wearing a pin's clothes.
  validation {
    condition     = can(regex("^v?[0-9]+\\.[0-9]+\\.[0-9]+(-[0-9A-Za-z.]+)?$", var.chart_version))
    error_message = "chart_version must be an exact chart version (e.g. \"1.2.3\" or \"v1.2.3\"); an empty value or a version range is resolved at apply time, which is not a pin."
  }
}

variable "ingress_class" {
  description = "IngressClass name the controller registers (referenced by the Helm chart's Ingress)."
  type        = string
  default     = "nginx"
}

variable "use_host_port" {
  description = <<-EOT
    Bind the controller to the node's host 80/443 (hostPort) and expose it via a
    NodePort Service instead of a LoadBalancer. This is the local-kind recipe: a
    LoadBalancer Service stays <pending> on kind unless cloud-provider-kind owns
    the host ports — which collides with kind's own 80/443 extraPortMappings — so
    the helm release never goes ready and the apply times out. With host ports the
    controller binds the ingress-ready node directly and is reachable at
    localhost:80/443 with no LoadBalancer. Leave false for real clouds (GKE/EKS/AKS),
    where a LoadBalancer Service is correct.
  EOT
  type        = bool
  default     = false
}

variable "aws_nlb" {
  description = <<-EOT
    EKS: annotate the controller's LoadBalancer Service for an AWS Network Load Balancer
    (service.beta.kubernetes.io/aws-load-balancer-type: nlb). Off by default, because it
    REPLACES an existing Classic Load Balancer and so changes the hostname clients resolve.
    It is what makes externalTrafficPolicy: Local deliver the client address on AWS: a
    LoadBalancer Service with no annotation gets a Classic balancer, which proxies TCP, so
    the controller sees the balancer's address either way. (PROXY protocol is the other way.)
  EOT
  type        = bool
  default     = false
}

variable "replicas" {
  description = <<-EOT
    Controller replicas. Forced to 1 with use_host_port (two pods cannot share a node's
    host ports). On a cloud LoadBalancer the Service keeps the client address
    (externalTrafficPolicy: Local), so the balancer sends traffic ONLY to nodes that run a
    controller pod: with one replica a rolling update leaves exactly one target, and the
    balancer learns of the move through its health check, not instantly. Two or more
    replicas (spread across nodes by a preferred anti-affinity) keep a target throughout.
  EOT
  type        = number
  default     = 1

  validation {
    condition     = var.replicas >= 1 && var.replicas == floor(var.replicas)
    error_message = "replicas must be a whole number of at least 1."
  }
}

locals {
  replicas = var.use_host_port ? 1 : var.replicas

  ingress_controller_base = {
    replicaCount = local.replicas
    # 🔴 KEEP THE CLIENT'S ADDRESS. The chart's default, Cluster, makes kube-proxy SNAT every
    # external connection to the node that received it, so the "real TCP peer" the controller
    # writes into X-Forwarded-For (use-forwarded-headers is false, below) is a NODE address:
    # every device on the internet shares one rate-limit bucket per node, and a connection
    # that lands on the controller's own node is SNATed to that node's pod-range gateway,
    # which is inside infrastructure.http.trustedProxies and so is skipped as a proxy hop.
    # Local keeps the source address ONLY behind a balancer that passes the connection
    # through (GKE, AKS, an AWS NLB). An AWS Classic balancer, which EKS gives an
    # unannotated Service, proxies TCP and defeats it: see var.aws_nlb.
    # Local keeps the source address, and the balancer health-checks the Service's
    # healthCheckNodePort so it sends only to nodes that run a controller pod (the cloud
    # balancers provision that themselves). Only meaningful on a LoadBalancer Service: the
    # host-port recipe below replaces the whole `service` block with a NodePort, whose
    # traffic arrives on the node that holds the port and is not SNATed.
    service = merge(
      { externalTrafficPolicy = "Local" },
      var.aws_nlb ? { annotations = { "service.beta.kubernetes.io/aws-load-balancer-type" = "nlb" } } : {},
    )
    ingressClassResource = {
      name    = var.ingress_class
      enabled = true
    }
    ingressClass = var.ingress_class
    # 🔴 SNIPPETS STAY ON IN THIS RELEASE, ON PURPOSE, AND THE CHART NO LONGER NEEDS THEM.
    # This controller is SHARED by every instance on the cluster. The documented order
    # is `dcctl install` (this module) and then `dcctl upgrade` per instance, so for a
    # while there are instances still on an older chart whose `devicechain-api` Ingress
    # carries a configuration-snippet. A controller that refuses snippets skips the
    # whole Ingress, so every /api route of such an instance would go dark, and stay
    # dark for one whose upgrade is refused. The current chart sets no snippet (the
    # service-token mint is served on an internal listener no route reaches), and
    # hack/check-oauth-discovery-routing.sh fails a render that adds one.
    #
    # The controller is tightened in the NEXT release (allowSnippetAnnotations=false,
    # annotations-risk-level left at its default), behind a `dcctl install` preflight
    # that refuses while any Ingress of the controller's class still carries a
    # *-snippet annotation.
    #
    # The older instance Ingress uses a configuration-snippet annotation.
    # Upstream has been tightening that surface for several releases — snippets went
    # off by default, then a separate annotations risk level was added, and what that
    # level evaluates has changed again since. Both switches are re-enabled here, or
    # the instance chart's helm apply is rejected by the admission webhook ("Snippet
    # directives are disabled" / "risky annotation"). Local-dev trust boundary: the
    # only Ingress author is the DeviceChain chart itself.
    #
    # 🔴 No release boundary is named on purpose. The two that used to be written here
    # were wrong, and a version number in a comment is a claim about someone else's
    # changelog that nothing in this repo re-checks. What IS verified is the pin:
    # chart 4.15.1 / controller 1.15.1 admits the instance Ingress with these values,
    # measured on a live cluster. That is why the version is pinned rather than
    # tracked — so the next tightening arrives as a bump somebody chose.
    allowSnippetAnnotations = true
    config = {
      "annotations-risk-level" = "Critical"
      # Stated although it is the controller's default. The services key per-source limits on
      # the X-Forwarded-For this controller writes, and they believe it only from the
      # controller's own pods (infrastructure.http.trustedProxies): with this OFF the controller
      # writes the real TCP peer, and with it ON a caller's own header would win. A load balancer
      # in front that appends the header is the one reason to turn it on, together with
      # proxy-real-ip-cidr set to that balancer's ranges.
      "use-forwarded-headers" = "false"
    }
  }
  # Spread the replicas over nodes where there is more than one (Local sends traffic only
  # to nodes running a pod, so two pods on one node would be one target). Preferred, not
  # required: a one-node cluster must still schedule both.
  ingress_controller_spread = local.replicas > 1 ? { affinity = {
    podAntiAffinity = {
      preferredDuringSchedulingIgnoredDuringExecution = [{
        weight = 100
        podAffinityTerm = {
          topologyKey = "kubernetes.io/hostname"
          labelSelector = {
            matchLabels = {
              "app.kubernetes.io/name"      = "ingress-nginx"
              "app.kubernetes.io/instance"  = var.release_name
              "app.kubernetes.io/component" = "controller"
            }
          }
        }
      }]
    }
  } } : {}
  # kind recipe: bind node 80/443 directly, no LoadBalancer to wait on.
  ingress_controller_host_port = {
    hostPort     = { enabled = true }
    service      = { type = "NodePort" }
    nodeSelector = { "ingress-ready" = "true" }
    tolerations = [{
      key      = "node-role.kubernetes.io/control-plane"
      operator = "Equal"
      effect   = "NoSchedule"
    }]
  }
}

resource "helm_release" "ingress_nginx" {
  max_history      = local.helm_max_history
  name             = var.release_name
  namespace        = var.namespace
  create_namespace = true
  repository       = "https://kubernetes.github.io/ingress-nginx"
  chart            = "ingress-nginx"
  version          = var.chart_version

  # yamlencode is inside the conditional so both branches are strings (an HCL
  # ternary requires both arms to share a type; the merged host-port object and
  # the base object do not).
  values = [
    var.use_host_port
    ? yamlencode({ controller = merge(local.ingress_controller_base, local.ingress_controller_host_port) })
    : yamlencode({ controller = merge(local.ingress_controller_base, local.ingress_controller_spread) })
  ]
}

output "ingress_class" {
  description = "IngressClass name to set on Ingress resources."
  value       = var.ingress_class
}

output "namespace" {
  value = var.namespace
}
