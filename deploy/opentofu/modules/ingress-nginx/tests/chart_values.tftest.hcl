# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# The ingress-nginx chart values the platform's behaviour rests on.
#
# A key the chart ignores does not fail a `helm install`: the install comes up healthy
# WITHOUT the behaviour. externalTrafficPolicy is that kind of key. With the chart's
# default (Cluster) kube-proxy SNATs every external connection to a node address, so
# the per-source limiter (infrastructure.http.trustedProxies) buckets every internet
# device into one bucket per node. Each check decodes the document OUTSIDE try(), so a
# wrong address fails the run rather than reading as "absent". Runs against mocked
# providers, no cluster needed. Run it with hack/check-tofu-module-tests.sh.

mock_provider "helm" {}

run "a_cloud_install_keeps_the_client_address" {
  command = plan

  assert {
    condition     = yamldecode(helm_release.ingress_nginx.values[0]).controller.service.externalTrafficPolicy == "Local"
    error_message = "externalTrafficPolicy must be Local so the real client address reaches the controller"
  }

  assert {
    condition     = !can(yamldecode(helm_release.ingress_nginx.values[0]).controller.service.type)
    error_message = "a cloud install leaves the Service type to the chart's default LoadBalancer"
  }

  assert {
    condition     = yamldecode(helm_release.ingress_nginx.values[0]).controller.config["use-forwarded-headers"] == "false"
    error_message = "the services believe the controller's X-Forwarded-For only while the controller writes the real peer into it"
  }

  assert {
    condition     = yamldecode(helm_release.ingress_nginx.values[0]).controller.replicaCount == 1 && !can(yamldecode(helm_release.ingress_nginx.values[0]).controller.affinity)
    error_message = "one replica by default, with no anti-affinity to apply"
  }
}

run "eks_nlb_is_opt_in" {
  command = plan

  assert {
    condition     = !can(yamldecode(helm_release.ingress_nginx.values[0]).controller.service.annotations)
    error_message = "the AWS NLB annotation must be off by default: it replaces an existing load balancer"
  }
}

run "eks_nlb_annotation" {
  command = plan

  variables {
    aws_nlb = true
  }

  assert {
    condition     = yamldecode(helm_release.ingress_nginx.values[0]).controller.service.annotations["service.beta.kubernetes.io/aws-load-balancer-type"] == "nlb" && yamldecode(helm_release.ingress_nginx.values[0]).controller.service.externalTrafficPolicy == "Local"
    error_message = "aws_nlb must add the NLB type annotation next to Local"
  }
}

run "more_replicas_are_spread_over_nodes" {
  command = plan

  variables {
    replicas = 3
  }

  assert {
    condition     = yamldecode(helm_release.ingress_nginx.values[0]).controller.replicaCount == 3
    error_message = "replicas must reach the chart"
  }

  assert {
    condition     = yamldecode(helm_release.ingress_nginx.values[0]).controller.affinity.podAntiAffinity.preferredDuringSchedulingIgnoredDuringExecution[0].podAffinityTerm.topologyKey == "kubernetes.io/hostname"
    error_message = "with Local only nodes running a pod are targets, so replicas must prefer different nodes"
  }
}

run "host_port_mode_is_the_kind_recipe" {
  command = plan

  variables {
    use_host_port = true
    replicas      = 3
  }

  assert {
    condition     = yamldecode(helm_release.ingress_nginx.values[0]).controller.service.type == "NodePort"
    error_message = "the kind recipe is a NodePort Service"
  }

  assert {
    condition     = !can(yamldecode(helm_release.ingress_nginx.values[0]).controller.service.externalTrafficPolicy)
    error_message = "externalTrafficPolicy is only meaningful on a LoadBalancer here"
  }

  assert {
    condition     = yamldecode(helm_release.ingress_nginx.values[0]).controller.replicaCount == 1
    error_message = "two pods cannot share a node's host ports, so host-port mode forces one replica"
  }
}

# --- hitless rollouts behind a cloud balancer (D-SR3) ------------------------------

run "a_cloud_install_drains_before_it_leaves" {
  command = plan

  assert {
    condition     = yamldecode(helm_release.ingress_nginx.values[0]).controller.extraArgs["shutdown-grace-period"] == 40
    error_message = "the controller must keep serving for at least the balancer's health-check failure time (3 x 8s on GCP) after SIGTERM"
  }

  assert {
    condition     = yamldecode(helm_release.ingress_nginx.values[0]).controller.terminationGracePeriodSeconds > yamldecode(helm_release.ingress_nginx.values[0]).controller.extraArgs["shutdown-grace-period"]
    error_message = "terminationGracePeriodSeconds must exceed the shutdown grace period or the kubelet kills the pod mid-drain"
  }

  assert {
    condition     = yamldecode(helm_release.ingress_nginx.values[0]).controller.minReadySeconds == 15
    error_message = "minReadySeconds gives the balancer time to see the new pod's node healthy before the old pod goes"
  }
}

run "drain_can_be_turned_off" {
  command = plan

  variables {
    drain_seconds     = 0
    min_ready_seconds = 0
  }

  assert {
    condition     = !can(yamldecode(helm_release.ingress_nginx.values[0]).controller.extraArgs) && !can(yamldecode(helm_release.ingress_nginx.values[0]).controller.minReadySeconds)
    error_message = "0 must leave the chart's defaults alone"
  }
}

run "kind_host_port_does_not_drain" {
  command = plan

  variables {
    use_host_port = true
  }

  assert {
    condition     = !can(yamldecode(helm_release.ingress_nginx.values[0]).controller.extraArgs) && !can(yamldecode(helm_release.ingress_nginx.values[0]).controller.minReadySeconds)
    error_message = "host-port mode (kind) must not drain: no balancer, and the wait would slow every rollout"
  }
}
