# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# The broker runs DeviceChain's own build, as root like the upstream image, with its metrics
# port declared — and refuses to render without naming a version.
#
# The nats chart has no values schema, so an image map, a container patch or a pod
# securityContext under the wrong parent is accepted and does nothing: the chart's own
# upstream image runs (and no device can authenticate by certificate), or the image's
# non-root default user meets the root-owned JetStream store an upstream server wrote.
# This reads the values document Helm is handed, so a key that drifts is a failure here.
#
# Runs against mocked providers, so no cluster is needed. Run it with
# hack/check-tofu-module-tests.sh, which tests a COPY of the module.

mock_provider "helm" {}
mock_provider "kubernetes" {}

variables {
  namespace        = "dci-test"
  server_image_tag = "v0.26.0"
  enable_tls       = false
  enable_auth      = false
}

run "our_image_by_default" {
  command = plan

  assert {
    condition = try(yamldecode(helm_release.nats.values[0]).container.image, null) == {
      repository = "ghcr.io/devicechain-io/dc-nats-server"
      tag        = "v0.26.0"
      digest     = null
    }
    error_message = "the broker must be ghcr.io/devicechain-io/dc-nats-server at the tag passed, under container.image, with no digest"
  }

  assert {
    condition     = output.server_image.image.repository == "ghcr.io/devicechain-io/dc-nats-server" && output.server_image.run_as_user == 0
    error_message = "the server_image output must report the image and runAsUser 0 read back from the values"
  }

  # The server container runs as root, as upstream's does, so it can open a JetStream store
  # an upstream server wrote (0700/0600, root-owned).
  assert {
    condition = try(yamldecode(helm_release.nats.values[0]).container.merge.securityContext, null) == {
      runAsUser  = 0
      runAsGroup = 0
    }
    error_message = "the nats container must carry securityContext runAsUser 0 / runAsGroup 0 under container.merge"
  }

  # And no pod fsGroup: it would recursively re-own the volume on every start, and does not
  # apply on hostPath at all.
  assert {
    condition     = try(yamldecode(helm_release.nats.values[0]).podTemplate.merge, null) == null
    error_message = "podTemplate.merge must not be set (no fsGroup)"
  }

  # The broker's own /metrics, on the named port the instance chart's PodMonitor selects.
  assert {
    condition = try(yamldecode(helm_release.nats.values[0]).container.patch, null) == [{
      op    = "add"
      path  = "/ports/-"
      value = { name = "dc-metrics", containerPort = 7778 }
    }]
    error_message = "container.patch must append the dc-metrics container port (7778)"
  }

  # The checksum annotation the module depends on to adopt config changes is still set.
  assert {
    condition     = try(yamldecode(helm_release.nats.values[0]).podTemplate.configChecksumAnnotation, null) == true
    error_message = "podTemplate.configChecksumAnnotation must still be true next to podTemplate.merge"
  }
}

run "a_digest_is_passed_through" {
  command = plan

  variables {
    server_image_digest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
  }

  assert {
    condition     = try(yamldecode(helm_release.nats.values[0]).container.image.digest, null) == "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
    error_message = "a digest must reach container.image.digest, where the chart uses it in place of the tag"
  }
}

# The documented rollback renders: the upstream image, by repository and tag.
run "the_upstream_rollback_renders" {
  command = plan

  variables {
    server_image_repository = "nats"
    server_image_tag        = "2.15.1-alpine"
  }

  assert {
    condition     = try(yamldecode(helm_release.nats.values[0]).container.image.repository, null) == "nats" && try(yamldecode(helm_release.nats.values[0]).container.image.tag, null) == "2.15.1-alpine"
    error_message = "the upstream rollback (nats:2.15.1-alpine) must reach container.image"
  }
}

# No tag and no digest: refused at plan, rather than rendering "repository:" (read as :latest).
run "no_version_is_refused" {
  command = plan

  variables {
    server_image_tag = ""
  }

  expect_failures = [helm_release.nats]
}

run "a_tag_inside_the_repository_is_refused" {
  command = plan

  variables {
    server_image_repository = "ghcr.io/devicechain-io/dc-nats-server:v0.26.0"
  }

  expect_failures = [var.server_image_repository]
}

run "a_malformed_digest_is_refused" {
  command = plan

  variables {
    server_image_digest = "sha256:abc"
  }

  expect_failures = [var.server_image_digest]
}
