# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# The playground driver: seeds the blueprint tenants, keeps their simulators feeding
# them, and resets them every night. The policy is in backend/tools/playground; this file
# is where it runs.
#
# HOW A NIGHTLY RESET WORKS, because the obvious design cannot. A deleted tenant's token
# is held for at least 12 hours, so "delete the tenants and create them again" is refused
# by the platform. The driver rotates through `slots` sets of tenants instead: at each
# reset it creates today's slot from the fixed seed, then destroys every other slot. A
# visitor's changes live in yesterday's tenants, which are destroyed. The reset CronJob's
# only job is to roll the driver Deployment; a freshly started driver rotates.

locals {
  reset_schedule = "5 ${var.reset_hour_utc} * * *"

  driver_labels = {
    "app.kubernetes.io/name"      = "playground-driver"
    "app.kubernetes.io/component" = "driver"
    "app.kubernetes.io/part-of"   = "devicechain-playground"
  }

  driver_name = "playground-driver"

  # What both `dcctl sim create` and `dcctl sim destroy` take: where the instance is.
  # --server is always present: dcctl sim defaults to localhost, which from a pod is the
  # pod.
  reach_args = join(" ", concat(
    ["--server", var.sim_server, "--instance", var.instance_id],
    var.sim_host_header != "" ? ["--host-header", var.sim_host_header] : [],
    var.sim_tls ? ["--tls"] : [],
  ))

  # What `dcctl tenant-deletion status` takes to ask whether a sim's tenant still exists
  # (the driver does that when dcctl has lost a sim's record). It has no --host-header, so
  # an instance reached by address with a Host override cannot be probed, and the driver
  # then keeps the sim's ledger entry rather than guess.
  probe_args = join(" ", concat(
    ["--server", var.sim_server, "--email", "superuser@devicechain.local"],
    var.sim_tls ? ["--tls"] : [],
  ))

  # What `dcctl sim create` alone takes. destroy refuses these as unknown flags.
  create_args = join(" ", var.sim_create_args)

  driver_enabled = var.deploy_driver ? 1 : 0
}

resource "kubernetes_namespace_v1" "driver" {
  count = local.driver_enabled

  metadata {
    name = var.namespace
    labels = merge(local.driver_labels, {
      "pod-security.kubernetes.io/enforce" = "restricted"
    })
  }

  lifecycle {
    precondition {
      condition     = trimspace(var.driver_image) != ""
      error_message = "deploy_driver needs driver_image: build image/Dockerfile and push it to a registry you own."
    }
    precondition {
      condition     = trimspace(var.sim_server) != ""
      error_message = "deploy_driver needs sim_server: the host the driver reaches the instance at from inside the cluster."
    }
    precondition {
      condition     = trimspace(var.superuser_password) != ""
      error_message = "deploy_driver needs superuser_password: dcctl sim create signs in with it."
    }
  }
}

resource "kubernetes_secret_v1" "superuser" {
  count = local.driver_enabled

  metadata {
    name      = "playground-superuser"
    namespace = kubernetes_namespace_v1.driver[0].metadata[0].name
    labels    = local.driver_labels
  }

  data = {
    DC_ADMIN_PASSWORD = var.superuser_password
  }
}

# The sim records (each holds a scoped identity's password) must outlive a pod, because
# tonight's reset destroys yesterday's sims by reading their records.
resource "kubernetes_persistent_volume_claim_v1" "home" {
  count = local.driver_enabled

  metadata {
    name      = "playground-home"
    namespace = kubernetes_namespace_v1.driver[0].metadata[0].name
    labels    = local.driver_labels
  }

  spec {
    access_modes = ["ReadWriteOnce"]
    resources {
      requests = {
        storage = "100Mi"
      }
    }
  }

  # WaitForFirstConsumer storage classes leave the claim Pending until the Deployment
  # below schedules a pod, which is created after this resource.
  wait_until_bound = false
}

resource "kubernetes_service_account_v1" "driver" {
  count = local.driver_enabled

  metadata {
    name      = local.driver_name
    namespace = kubernetes_namespace_v1.driver[0].metadata[0].name
    labels    = local.driver_labels
  }

  # The driver pod calls no Kubernetes API; only the reset job does.
  automount_service_account_token = false
}

resource "kubernetes_service_account_v1" "reset" {
  count = local.driver_enabled

  metadata {
    name      = "playground-reset"
    namespace = kubernetes_namespace_v1.driver[0].metadata[0].name
    labels    = local.driver_labels
  }
}

# The reset job may do exactly one thing: read and patch the driver Deployment, by name.
resource "kubernetes_role_v1" "reset" {
  count = local.driver_enabled

  metadata {
    name      = "playground-reset"
    namespace = kubernetes_namespace_v1.driver[0].metadata[0].name
    labels    = local.driver_labels
  }

  rule {
    api_groups     = ["apps"]
    resources      = ["deployments"]
    resource_names = [local.driver_name]
    verbs          = ["get", "patch"]
  }
}

resource "kubernetes_role_binding_v1" "reset" {
  count = local.driver_enabled

  metadata {
    name      = "playground-reset"
    namespace = kubernetes_namespace_v1.driver[0].metadata[0].name
    labels    = local.driver_labels
  }

  role_ref {
    api_group = "rbac.authorization.k8s.io"
    kind      = "Role"
    name      = kubernetes_role_v1.reset[0].metadata[0].name
  }

  subject {
    kind      = "ServiceAccount"
    name      = kubernetes_service_account_v1.reset[0].metadata[0].name
    namespace = kubernetes_namespace_v1.driver[0].metadata[0].name
  }
}

resource "kubernetes_deployment_v1" "driver" {
  count = local.driver_enabled

  metadata {
    name      = local.driver_name
    namespace = kubernetes_namespace_v1.driver[0].metadata[0].name
    labels    = local.driver_labels
  }

  # The driver is waited on by the CronJob's rollout, not by the apply: it cannot become
  # ready until the instance accepts its sims, and a half-installed instance should not
  # hang the apply for the provider's ten-minute default.
  wait_for_rollout = false

  spec {
    # ONE driver, and Recreate so a roll never runs two: two drivers would each create the
    # same sims and fight over their control ports.
    replicas = 1

    strategy {
      type = "Recreate"
    }

    selector {
      match_labels = {
        "app.kubernetes.io/name" = local.driver_labels["app.kubernetes.io/name"]
      }
    }

    template {
      metadata {
        labels = local.driver_labels
      }

      spec {
        service_account_name            = kubernetes_service_account_v1.driver[0].metadata[0].name
        automount_service_account_token = false

        security_context {
          run_as_non_root = true
          run_as_user     = 65532
          fs_group        = 65532
          seccomp_profile {
            type = "RuntimeDefault"
          }
        }

        container {
          name  = "driver"
          image = var.driver_image
          args  = ["run"]

          env {
            name  = "HOME"
            value = "/home/playground"
          }
          env {
            name  = "DC_PLAYGROUND_SCENARIOS"
            value = join(",", var.scenarios)
          }
          env {
            name  = "DC_PLAYGROUND_SLOTS"
            value = tostring(var.slots)
          }
          env {
            name  = "DC_PLAYGROUND_RESET_HOUR"
            value = tostring(var.reset_hour_utc)
          }
          env {
            name  = "DC_PLAYGROUND_SEED"
            value = tostring(var.seed)
          }
          env {
            name  = "DC_PLAYGROUND_REACH_ARGS"
            value = local.reach_args
          }
          env {
            name  = "DC_PLAYGROUND_CREATE_ARGS"
            value = local.create_args
          }
          env {
            name  = "DC_PLAYGROUND_PROBE_ARGS"
            value = local.probe_args
          }
          env {
            name = "DC_ADMIN_PASSWORD"
            value_from {
              secret_key_ref {
                name = kubernetes_secret_v1.superuser[0].metadata[0].name
                key  = "DC_ADMIN_PASSWORD"
              }
            }
          }

          resources {
            requests = {
              cpu    = "100m"
              memory = "128Mi"
            }
            limits = {
              memory = "512Mi"
            }
          }

          # run_as_non_root is stated on the CONTAINER as well as the pod. The provider
          # writes a container security context's unset booleans as false, and the
          # restricted pod security level this namespace enforces refuses a container that
          # says runAsNonRoot is false whatever the pod says.
          security_context {
            allow_privilege_escalation = false
            read_only_root_filesystem  = true
            run_as_non_root            = true
            run_as_user                = 65532
            capabilities {
              drop = ["ALL"]
            }
          }

          volume_mount {
            name       = "home"
            mount_path = "/home/playground"
          }
          volume_mount {
            name       = "tmp"
            mount_path = "/tmp"
          }
        }

        volume {
          name = "home"
          persistent_volume_claim {
            claim_name = kubernetes_persistent_volume_claim_v1.home[0].metadata[0].name
          }
        }
        volume {
          name = "tmp"
          empty_dir {}
        }
      }
    }
  }

  lifecycle {
    # The reset job stamps the pod template's annotations on every run; an apply that
    # then "restored" them would roll the driver and reset the playground by hand.
    ignore_changes = [spec[0].template[0].metadata[0].annotations]
  }
}

resource "kubernetes_cron_job_v1" "reset" {
  count = local.driver_enabled

  metadata {
    name      = "playground-reset"
    namespace = kubernetes_namespace_v1.driver[0].metadata[0].name
    labels    = local.driver_labels
  }

  spec {
    schedule = local.reset_schedule
    # Five minutes past the playground's day boundary (the driver's reset hour), never
    # before it: a roll that landed early would start the driver on the OLD day and
    # reset nothing.
    timezone                      = "Etc/UTC"
    concurrency_policy            = "Forbid"
    starting_deadline_seconds     = 3600
    successful_jobs_history_limit = 3
    failed_jobs_history_limit     = 3

    job_template {
      metadata {
        labels = local.driver_labels
      }
      spec {
        backoff_limit = 3
        template {
          metadata {
            labels = merge(local.driver_labels, { "app.kubernetes.io/name" = "playground-reset", "app.kubernetes.io/component" = "reset" })
          }
          spec {
            service_account_name = kubernetes_service_account_v1.reset[0].metadata[0].name
            restart_policy       = "OnFailure"

            security_context {
              run_as_non_root = true
              run_as_user     = 65532
              seccomp_profile {
                type = "RuntimeDefault"
              }
            }

            container {
              name  = "reset"
              image = var.driver_image
              args  = ["restart", "--deployment", local.driver_name]

              security_context {
                allow_privilege_escalation = false
                read_only_root_filesystem  = true
                run_as_non_root            = true
                run_as_user                = 65532
                capabilities {
                  drop = ["ALL"]
                }
              }

              resources {
                requests = {
                  cpu    = "10m"
                  memory = "32Mi"
                }
                limits = {
                  memory = "64Mi"
                }
              }
            }
          }
        }
      }
    }
  }
}

# The driver may reach DNS and the instance's namespace, and nothing else: it is the one
# pod in this cluster that holds the superuser password.
resource "kubernetes_network_policy_v1" "driver" {
  count = var.deploy_driver && var.network_policy ? 1 : 0

  metadata {
    name      = "playground-driver"
    namespace = kubernetes_namespace_v1.driver[0].metadata[0].name
    labels    = local.driver_labels
  }

  spec {
    pod_selector {
      match_labels = {
        "app.kubernetes.io/name" = local.driver_labels["app.kubernetes.io/name"]
      }
    }
    policy_types = ["Ingress", "Egress"]

    egress {
      ports {
        port     = "53"
        protocol = "UDP"
      }
      ports {
        port     = "53"
        protocol = "TCP"
      }
    }
    egress {
      to {
        namespace_selector {
          match_labels = {
            "kubernetes.io/metadata.name" = var.instance_namespace
          }
        }
      }
    }
    # The ingress controller is the usual --server; it lives in its own namespace.
    egress {
      to {
        namespace_selector {
          match_labels = {
            "kubernetes.io/metadata.name" = var.ingress_namespace
          }
        }
      }
    }
  }
}

# The reset job needs the API server and nothing else. Its address is not known to the
# chart and differs per cluster, so the rule is the API server's ports on any
# destination, plus DNS.
resource "kubernetes_network_policy_v1" "reset" {
  count = var.deploy_driver && var.network_policy ? 1 : 0

  metadata {
    name      = "playground-reset"
    namespace = kubernetes_namespace_v1.driver[0].metadata[0].name
    labels    = local.driver_labels
  }

  spec {
    pod_selector {
      match_labels = {
        "app.kubernetes.io/component" = "reset"
      }
    }
    policy_types = ["Ingress", "Egress"]

    egress {
      ports {
        port     = "53"
        protocol = "UDP"
      }
      ports {
        port     = "53"
        protocol = "TCP"
      }
    }
    egress {
      ports {
        port     = "443"
        protocol = "TCP"
      }
      ports {
        port     = "6443"
        protocol = "TCP"
      }
    }
  }
}
