# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# The Traefik chart values the platform's behaviour rests on.
#
# A key the chart renames or drops does not fail a `helm install`: the value is silently
# ignored and the install comes up healthy WITHOUT the behaviour. That is not a
# hypothetical here -- `service.type` (instead of `service.spec.type`) is accepted and
# ignored by the chart's schema, so a LoadBalancer Service on kind sat <pending> until
# `helm --wait` timed out. Each key below was checked against the PINNED chart's rendered
# output (container args, the Service, the Deployment's strategy and ports) on a kind
# cluster, and this pins that the module still hands the chart those keys. The rendering
# check is the one to repeat on a chart bump:
#   helm template traefik traefik/traefik --version <pin> -f <values> --kube-version 1.34.0
#
# Every check decodes the document OUTSIDE try(), so a wrong address fails the run rather
# than reading as "absent". Runs against mocked providers, no cluster needed. Run it with
# hack/check-tofu-module-tests.sh.
#
# The read_timeout_* runs are also read by a Go test in update-management/model, which
# recomputes each expectation from the service's own UploadWindow and claim grace. Keep
# each as `max_artifact_bytes = N` plus `output.read_timeout == "Ms"` in one run block.

mock_provider "helm" {}

run "the_pins" {
  command = plan

  assert {
    condition     = helm_release.traefik.version == "42.0.0"
    error_message = "the chart pin must be 42.0.0 (Traefik v3.7.14); see the variable for why 3.7.15/16 were not taken"
  }

  assert {
    condition     = yamldecode(helm_release.traefik.values[0]).image.tag == "v3.7.14"
    error_message = "the image tag must be stated and must be the chart's appVersion"
  }

  assert {
    condition     = yamldecode(helm_release.traefik.values[0]).image.registry == "docker.io" && yamldecode(helm_release.traefik.values[0]).image.repository == "traefik"
    error_message = "the image must come from the registry/repository variables, so a mirrored install can move it"
  }

  assert {
    condition     = helm_release.traefik.max_history == 10
    error_message = "the release must bound its revision history"
  }
}

run "the_image_tag_and_digest_are_one_pin" {
  command = plan

  assert {
    condition     = yamldecode(helm_release.traefik.values[0]).image.digest == "sha256:575fa15b135078fe5e50aa847987d96dbddd7b093c172429618404df73f3fa7c"
    error_message = "the digest must be the index digest of traefik:v3.7.14; a tag bump that leaves it behind pulls the old image under a new version string"
  }

  assert {
    condition     = yamldecode(helm_release.traefik.values[0]).versionOverride == "v3.7.14"
    error_message = "with a digest the chart cannot derive the version, so it must be told: versionOverride = image_tag"
  }
}

run "no_digest_pulls_by_tag_alone" {
  command = plan

  variables {
    image_digest = ""
  }

  assert {
    condition     = !can(yamldecode(helm_release.traefik.values[0]).image.digest) && yamldecode(helm_release.traefik.values[0]).versionOverride == ""
    error_message = "an empty digest must leave the chart pulling by tag, with no version override"
  }
}

run "it_does_not_take_over_the_cluster" {
  command = plan

  assert {
    condition     = yamldecode(helm_release.traefik.values[0]).ingressClass.isDefaultClass == false
    error_message = "the IngressClass must not be the cluster default: it would hijack every class-less Ingress, including ones another controller serves"
  }

  assert {
    condition     = yamldecode(helm_release.traefik.values[0]).providers.kubernetesIngress.ingressClass == "traefik"
    error_message = "the Ingress provider must be scoped to its own class so it never serves another controller's Ingresses"
  }

  assert {
    condition     = yamldecode(helm_release.traefik.values[0]).providers.kubernetesCRD.enabled == false
    error_message = "the CRD provider must be off: routing is Gateway API only, and Middleware/IngressRoute would let a namespace attach controller-specific behaviour"
  }

  assert {
    condition     = yamldecode(helm_release.traefik.values[0]).providers.kubernetesGateway.enabled == true
    error_message = "the Gateway provider is the point of the controller"
  }

  assert {
    condition     = yamldecode(helm_release.traefik.values[0]).gateway.enabled == false
    error_message = "instances render their own Gateways; the chart's default one would claim a listener nobody asked for"
  }
}

run "entrypoints_are_the_standard_ports" {
  command = plan

  assert {
    condition     = yamldecode(helm_release.traefik.values[0]).ports.web.port == 80 && yamldecode(helm_release.traefik.values[0]).ports.websecure.port == 443
    error_message = "Gateway listeners are matched to ENTRYPOINT ports, so the entrypoints must be 80 and 443 for the instance chart to render the standard listener ports"
  }
}

# --- the timeouts ----------------------------------------------------------------

run "read_timeout_default_is_the_upload_window_plus_the_grace" {
  command = plan

  # 512 MiB: 2m + 2048s + 1m
  assert {
    condition     = output.read_timeout == "2228s"
    error_message = "the default read timeout must be UploadWindow(512 MiB) + claim grace = 2228s; got ${output.read_timeout}"
  }
}

run "read_timeout_1gib" {
  command = plan

  variables {
    max_artifact_bytes = 1073741824
  }

  # 120 + 4096 + 60
  assert {
    condition     = output.read_timeout == "4276s"
    error_message = "1 GiB must give 4276s; got ${output.read_timeout}"
  }
}

run "read_timeout_floors_a_partial_second" {
  command = plan

  variables {
    max_artifact_bytes = 262145
  }

  # floor(262145 / 262144) = 1, like the service's integer division
  assert {
    condition     = output.read_timeout == "181s"
    error_message = "a size one byte over 256 KiB must give 181s (the service floors the division); got ${output.read_timeout}"
  }
}

run "read_timeout_one_byte" {
  command = plan

  variables {
    max_artifact_bytes = 1
  }

  assert {
    condition     = output.read_timeout == "180s"
    error_message = "the fixed allowance and the grace are the floor: 180s; got ${output.read_timeout}"
  }
}

run "every_entrypoint_gets_the_same_timeouts" {
  command = plan

  assert {
    condition = alltrue([
      for ep in ["web", "websecure"] :
      yamldecode(helm_release.traefik.values[0]).ports[ep].transport.respondingTimeouts.readTimeout == "2228s"
    ])
    error_message = "both entrypoints must carry the derived read timeout: the default 60s cuts a large OTA upload at exactly 60 seconds"
  }

  assert {
    condition = alltrue([
      for ep in ["web", "websecure"] :
      yamldecode(helm_release.traefik.values[0]).ports[ep].transport.respondingTimeouts.writeTimeout == "0"
    ])
    error_message = "writeTimeout must be 0: downloads and WebSockets stream, and the service arms its own per-chunk write deadlines"
  }

  assert {
    condition = alltrue([
      for ep in ["web", "websecure"] :
      yamldecode(helm_release.traefik.values[0]).ports[ep].transport.respondingTimeouts.idleTimeout == "180s"
    ])
    error_message = "idleTimeout must be stated: 180s"
  }

  assert {
    condition     = local.read_timeout != "60s" && local.read_timeout != "0s" && local.read_timeout != "0"
    error_message = "the read timeout is a two-sided pin: 60s cuts uploads, 0 leaves a header-dribbling client unbounded"
  }
}

run "the_graphql_edge_is_the_response_header_timeout" {
  command = plan

  assert {
    condition     = contains(yamldecode(helm_release.traefik.values[0]).additionalArguments, "--serversTransport.forwardingTimeouts.responseHeaderTimeout=60s")
    error_message = "the 60s GraphQL edge must be configured: Traefik's default is none, and HTTPRoute timeouts are not implemented"
  }

  assert {
    condition     = local.response_header_timeout_seconds == 60
    error_message = "response_header_timeout_seconds must stay 60: it restates core/config.RequestEdgeTimeout, which a Go test pins"
  }
}

# --- the access log --------------------------------------------------------------

run "query_strings_are_dropped_from_the_access_log" {
  command = plan

  assert {
    condition     = yamldecode(helm_release.traefik.values[0]).accessLog.fields.queryParameters.defaultMode == "drop"
    error_message = "queryParameters.defaultMode must be drop: the OTA download capability travels in ?cap="
  }

  assert {
    condition     = yamldecode(helm_release.traefik.values[0]).accessLog.enabled == false
    error_message = "the access log is off by default"
  }
}

run "turning_the_access_log_on_keeps_the_drop" {
  command = plan

  variables {
    access_log_enabled = true
  }

  assert {
    condition     = yamldecode(helm_release.traefik.values[0]).accessLog.enabled == true && yamldecode(helm_release.traefik.values[0]).accessLog.fields.queryParameters.defaultMode == "drop"
    error_message = "enabling the log must not change the query drop"
  }
}

# --- forwarded headers -----------------------------------------------------------

run "the_edge_trusts_no_forwarded_headers" {
  command = plan

  assert {
    condition = alltrue([
      for ep in ["web", "websecure"] :
      length(yamldecode(helm_release.traefik.values[0]).ports[ep].forwardedHeaders.trustedIPs) == 0 && yamldecode(helm_release.traefik.values[0]).ports[ep].forwardedHeaders.insecure == false
    ])
    error_message = "at the edge no peer is trusted: Traefik must discard incoming X-Forwarded-* and reset X-Forwarded-For to the real peer"
  }

  assert {
    condition = alltrue([
      for ep in ["web", "websecure"] :
      yamldecode(helm_release.traefik.values[0]).ports[ep].http.aliasHeadersStrategy == "delete"
    ])
    error_message = "alias headers (X_Forwarded_For) must be deleted before routing"
  }
}

run "behind_a_load_balancer_the_ranges_are_trusted" {
  command = plan

  variables {
    trusted_proxy_cidrs = ["130.211.0.0/22", "35.191.0.0/16"]
  }

  assert {
    condition = alltrue([
      for ep in ["web", "websecure"] :
      yamldecode(helm_release.traefik.values[0]).ports[ep].forwardedHeaders.trustedIPs == ["130.211.0.0/22", "35.191.0.0/16"]
    ])
    error_message = "trusted_proxy_cidrs must reach both entrypoints"
  }
}

# --- the Service and the rollout --------------------------------------------------

run "a_cloud_install_is_a_local_load_balancer" {
  command = plan

  assert {
    condition     = yamldecode(helm_release.traefik.values[0]).service.spec.type == "LoadBalancer"
    error_message = "the cloud default is a LoadBalancer Service, under service.spec.type"
  }

  assert {
    condition     = yamldecode(helm_release.traefik.values[0]).service.spec.externalTrafficPolicy == "Local"
    error_message = "externalTrafficPolicy must be Local so the real client address reaches the controller"
  }

  assert {
    condition     = !can(yamldecode(helm_release.traefik.values[0]).service.type)
    error_message = "service.type must not be set: the chart ignores it silently, and the Service type belongs under service.spec"
  }

  assert {
    condition     = !can(yamldecode(helm_release.traefik.values[0]).ports.web.hostPort) && !can(yamldecode(helm_release.traefik.values[0]).updateStrategy)
    error_message = "a cloud install uses no hostPort and keeps the chart's rolling update"
  }
}

run "kind_side_by_side_is_a_node_port_with_no_host_port" {
  command = plan

  variables {
    service_type = "NodePort"
  }

  assert {
    condition     = yamldecode(helm_release.traefik.values[0]).service.spec.type == "NodePort"
    error_message = "a cluster with no load-balancer provider needs NodePort, or helm --wait hangs on a <pending> LoadBalancer"
  }

  assert {
    condition     = !can(yamldecode(helm_release.traefik.values[0]).service.spec.externalTrafficPolicy)
    error_message = "externalTrafficPolicy is only meaningful on a LoadBalancer here"
  }

  assert {
    condition     = !can(yamldecode(helm_release.traefik.values[0]).ports.web.hostPort)
    error_message = "without use_host_port the controller must not bind the node's 80/443, which ingress-nginx may hold"
  }
}

run "host_port_mode_is_the_kind_recipe" {
  command = plan

  variables {
    use_host_port = true
    replicas      = 3
  }

  assert {
    condition     = yamldecode(helm_release.traefik.values[0]).service.spec.type == "NodePort"
    error_message = "host-port mode is NodePort: a LoadBalancer stays <pending> on kind and hangs helm --wait"
  }

  assert {
    condition     = yamldecode(helm_release.traefik.values[0]).ports.web.hostPort == 80 && yamldecode(helm_release.traefik.values[0]).ports.websecure.hostPort == 443
    error_message = "host-port mode binds the node's 80 and 443"
  }

  assert {
    condition     = yamldecode(helm_release.traefik.values[0]).updateStrategy.rollingUpdate.maxUnavailable == 1 && yamldecode(helm_release.traefik.values[0]).updateStrategy.rollingUpdate.maxSurge == 0
    error_message = "maxUnavailable 1 / maxSurge 0: the chart's default (0/1) deadlocks every upgrade on a hostPort, the surge pod can never schedule"
  }

  assert {
    condition     = yamldecode(helm_release.traefik.values[0]).deployment.replicas == 1
    error_message = "two pods cannot share a node's host ports"
  }

  assert {
    condition     = yamldecode(helm_release.traefik.values[0]).nodeSelector["ingress-ready"] == "true"
    error_message = "host-port mode runs on the ingress-ready node kind maps the host ports to"
  }
}

# --- the Gateway allow-list -------------------------------------------------------

run "no_allow_list_by_default" {
  command = plan

  assert {
    condition     = !can(yamldecode(helm_release.traefik.values[0]).providers.kubernetesGateway.gateways)
    error_message = "the default accepts any Gateway of the class: a static list would have to name every instance"
  }
}

run "an_allow_list_reaches_the_provider" {
  command = plan

  variables {
    gateway_allow_list = [{ name = "devicechain", namespace = "dci-a" }]
  }

  assert {
    condition     = yamldecode(helm_release.traefik.values[0]).providers.kubernetesGateway.gateways[0].namespace == "dci-a"
    error_message = "the opt-in allow-list must reach providers.kubernetesGateway.gateways"
  }
}

# --- the validations --------------------------------------------------------------

run "a_floating_image_tag_is_refused" {
  command = plan

  variables {
    image_tag = "latest"
  }

  expect_failures = [var.image_tag]
}

run "a_chart_version_range_is_refused" {
  command = plan

  variables {
    chart_version = "42.0.0 - 43.0.0"
  }

  expect_failures = [var.chart_version]
}

run "a_bad_cidr_is_refused" {
  command = plan

  variables {
    trusted_proxy_cidrs = ["not-a-cidr"]
  }

  expect_failures = [var.trusted_proxy_cidrs]
}

run "a_digest_must_be_a_sha256" {
  command = plan

  variables {
    image_digest = "abc"
  }

  expect_failures = [var.image_digest]
}

run "a_digest_reaches_the_image" {
  command = plan

  variables {
    image_digest = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
  }

  assert {
    condition     = startswith(yamldecode(helm_release.traefik.values[0]).image.digest, "sha256:")
    error_message = "a configured digest must reach the chart"
  }
}
