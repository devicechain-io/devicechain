# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# What this root plans, against mocked providers: no credentials, no cluster. It holds
# the plan's SHAPE -- what exists, what is spot, how the reset is wired to the driver --
# which `validate` cannot see.

mock_provider "google" {}
mock_provider "kubernetes" {}

variables {
  # An applied driver needs these; the cluster half needs none of them.
  driver_image       = "registry.example/playground-driver:test"
  sim_server         = "ingress.example.svc"
  superuser_password = "not-a-real-password"
}

run "by_default_nothing_is_created" {
  command = plan

  assert {
    condition     = length(google_container_cluster.this) == 0 && length(google_container_node_pool.this) == 0 && length(google_billing_budget.this) == 0
    error_message = "With create_cluster false the root must not plan anything in Google Cloud."
  }
  assert {
    condition     = length(kubernetes_deployment_v1.driver) == 0 && length(kubernetes_cron_job_v1.reset) == 0 && length(kubernetes_namespace_v1.driver) == 0
    error_message = "With deploy_driver false the root must not plan anything in the cluster."
  }
}

run "the_cluster_is_one_spot_pool_with_a_budget" {
  command = plan

  variables {
    create_cluster     = true
    project_id         = "dc-playground-test"
    billing_account    = "AAAAAA-BBBBBB-CCCCCC"
    budget_alert_email = "ops@example.com"
  }

  assert {
    condition     = length(google_container_node_pool.this) == 1 && google_container_node_pool.this[0].node_config[0].spot == true
    error_message = "The playground should be exactly one node pool, on spot VMs."
  }
  assert {
    condition     = google_container_node_pool.this[0].node_count == 1
    error_message = "The pool should default to one node."
  }
  assert {
    condition     = google_container_cluster.this[0].deletion_protection == true
    error_message = "A public demo's cluster should be deletion-protected by default."
  }
  assert {
    condition     = google_billing_budget.this[0].amount[0].specified_amount[0].units == "300"
    error_message = "The budget should default to 300 dollars a month."
  }
  assert {
    condition = (
      join(",", sort([for r in google_billing_budget.this[0].threshold_rules : "${r.threshold_percent}/${r.spend_basis}"]))
      == "0.5/CURRENT_SPEND,0.9/CURRENT_SPEND,1/CURRENT_SPEND,1/FORECASTED_SPEND"
    )
    error_message = "The budget should alert at 50%, 90% and 100% of spend and at 100% of forecast."
  }
  assert {
    condition     = length(google_billing_budget.this[0].all_updates_rule[0].monitoring_notification_channels) == 1
    error_message = "The budget should notify the alert channel."
  }
}

run "spot_can_be_turned_off" {
  command = plan

  variables {
    create_cluster     = true
    project_id         = "dc-playground-test"
    billing_account    = "AAAAAA-BBBBBB-CCCCCC"
    budget_alert_email = "ops@example.com"
    spot               = false
  }

  assert {
    condition     = google_container_node_pool.this[0].node_config[0].spot == false
    error_message = "spot = false should plan an on-demand pool."
  }
}

# The cost bound is the budget alert. A cluster planned without one is a cluster with
# nothing watching its bill, so each missing input fails the plan.
run "a_cluster_without_a_billing_account_is_refused" {
  command = plan

  variables {
    create_cluster     = true
    project_id         = "dc-playground-test"
    budget_alert_email = "ops@example.com"
  }

  expect_failures = [terraform_data.cluster_inputs]
}

run "a_cluster_without_an_alert_email_is_refused" {
  command = plan

  variables {
    create_cluster  = true
    project_id      = "dc-playground-test"
    billing_account = "AAAAAA-BBBBBB-CCCCCC"
  }

  expect_failures = [terraform_data.cluster_inputs]
}

run "a_cluster_without_a_project_is_refused" {
  command = plan

  variables {
    create_cluster     = true
    billing_account    = "AAAAAA-BBBBBB-CCCCCC"
    budget_alert_email = "ops@example.com"
  }

  expect_failures = [terraform_data.cluster_inputs]
}

run "a_region_is_not_a_zone" {
  command = plan

  variables {
    location = "us-east4"
  }

  expect_failures = [var.location]
}

run "the_driver_is_one_recreated_pod_with_no_api_token" {
  command = plan

  variables {
    deploy_driver = true
  }

  assert {
    condition     = tostring(kubernetes_deployment_v1.driver[0].spec[0].replicas) == "1" && kubernetes_deployment_v1.driver[0].spec[0].strategy[0].type == "Recreate"
    error_message = "Two drivers would create the same sims twice: one replica, Recreate."
  }
  assert {
    condition     = kubernetes_deployment_v1.driver[0].spec[0].template[0].spec[0].automount_service_account_token == false
    error_message = "The driver calls no Kubernetes API and should not be handed a token."
  }
  assert {
    condition     = kubernetes_deployment_v1.driver[0].spec[0].template[0].spec[0].container[0].security_context[0].read_only_root_filesystem == true
    error_message = "The driver should run with a read-only root filesystem."
  }
}

# Found by applying the root to a kind cluster: the namespace enforces the restricted pod
# security level, and the provider writes a container security context's unset booleans as
# false, so a container that does not say runAsNonRoot itself is refused by admission with
# every pod-level setting correct.
run "every_container_says_it_runs_as_non_root" {
  command = plan

  variables {
    deploy_driver = true
  }

  assert {
    condition     = kubernetes_deployment_v1.driver[0].spec[0].template[0].spec[0].container[0].security_context[0].run_as_non_root == true
    error_message = "The driver container must set run_as_non_root itself."
  }
  assert {
    condition     = kubernetes_cron_job_v1.reset[0].spec[0].job_template[0].spec[0].template[0].spec[0].container[0].security_context[0].run_as_non_root == true
    error_message = "The reset container must set run_as_non_root itself."
  }
  assert {
    condition     = kubernetes_namespace_v1.driver[0].metadata[0].labels["pod-security.kubernetes.io/enforce"] == "restricted"
    error_message = "The namespace should enforce the restricted pod security level."
  }
}

run "the_password_is_in_the_secret_and_not_in_the_pod_spec" {
  command = plan

  variables {
    deploy_driver = true
  }

  assert {
    condition     = nonsensitive(kubernetes_secret_v1.superuser[0].data["DC_ADMIN_PASSWORD"]) == "not-a-real-password"
    error_message = "The superuser password should reach the Secret."
  }
  assert {
    condition     = length([for e in kubernetes_deployment_v1.driver[0].spec[0].template[0].spec[0].container[0].env : e if try(e.value == "not-a-real-password", false)]) == 0
    error_message = "The password must reach the container through the Secret, never as a literal env value."
  }
}

# The reset and the driver must agree on when the day begins. The CronJob fires five
# minutes after the hour the driver rotates at; a CronJob that fired before it would roll
# the driver onto the OLD day and reset nothing.
run "the_reset_follows_the_drivers_day_boundary" {
  command = plan

  variables {
    deploy_driver  = true
    reset_hour_utc = 7
  }

  assert {
    condition     = kubernetes_cron_job_v1.reset[0].spec[0].schedule == "5 7 * * *"
    error_message = "The CronJob should fire at 07:05 UTC for a day that begins at 07:00."
  }
  assert {
    condition = (
      [for e in kubernetes_deployment_v1.driver[0].spec[0].template[0].spec[0].container[0].env : e.value if e.name == "DC_PLAYGROUND_RESET_HOUR"][0]
      == split(" ", kubernetes_cron_job_v1.reset[0].spec[0].schedule)[1]
    )
    error_message = "The driver's reset hour and the CronJob's hour must be the same number."
  }
  assert {
    condition     = kubernetes_cron_job_v1.reset[0].spec[0].concurrency_policy == "Forbid"
    error_message = "Two resets at once would roll the driver twice."
  }
}

run "the_reset_job_can_only_patch_the_driver_deployment" {
  command = plan

  variables {
    deploy_driver = true
  }

  assert {
    condition     = length(kubernetes_role_v1.reset[0].rule) == 1 && one(kubernetes_role_v1.reset[0].rule).verbs == toset(["get", "patch"])
    error_message = "The reset job should hold exactly get and patch."
  }
  assert {
    condition     = one(kubernetes_role_v1.reset[0].rule).resource_names == toset(["playground-driver"]) && one(kubernetes_role_v1.reset[0].rule).resources == toset(["deployments"])
    error_message = "The reset job's role should name one Deployment and nothing else."
  }
}

run "the_driver_seeds_the_requested_scenarios_over_three_slots" {
  command = plan

  variables {
    deploy_driver = true
    scenarios     = ["sitepulse", "fleetpulse"]
  }

  assert {
    condition     = [for e in kubernetes_deployment_v1.driver[0].spec[0].template[0].spec[0].container[0].env : e.value if e.name == "DC_PLAYGROUND_SCENARIOS"][0] == "sitepulse,fleetpulse"
    error_message = "The driver should be handed the scenario list."
  }
  assert {
    condition     = [for e in kubernetes_deployment_v1.driver[0].spec[0].template[0].spec[0].container[0].env : e.value if e.name == "DC_PLAYGROUND_SLOTS"][0] == "3"
    error_message = "The driver should rotate through three slots by default."
  }
}

run "a_driver_without_an_image_is_refused" {
  command = plan

  variables {
    deploy_driver = true
    driver_image  = ""
  }

  expect_failures = [kubernetes_namespace_v1.driver]
}

run "two_slots_are_refused" {
  command = plan

  variables {
    slots = 2
  }

  expect_failures = [var.slots]
}

run "an_hour_past_midnight_is_refused" {
  command = plan

  variables {
    reset_hour_utc = 24
  }

  expect_failures = [var.reset_hour_utc]
}

run "towerpulse_is_not_a_playground_scenario" {
  command = plan

  variables {
    scenarios = ["sitepulse", "towerpulse"]
  }

  expect_failures = [var.scenarios]
}

# dcctl sim destroy has fewer flags than sim create and refuses the rest as unknown, which
# a kind cluster showed: the first night's destroys all failed on --ingress. The flags only
# create takes must never reach the destroy's arguments.
run "create_only_flags_are_kept_out_of_the_shared_ones" {
  command = plan

  variables {
    deploy_driver   = true
    sim_host_header = "devicechain.local"
    sim_tls         = true
    sim_create_args = ["--ingress", "http://ingress.example:8081", "--mqtt-broker", "tcp://broker.example:1883"]
  }

  assert {
    condition = (
      [for e in kubernetes_deployment_v1.driver[0].spec[0].template[0].spec[0].container[0].env : e.value if e.name == "DC_PLAYGROUND_REACH_ARGS"][0]
      == "--server ingress.example.svc --instance playground --host-header devicechain.local --tls"
    )
    error_message = "The shared arguments should name the instance, the Host and TLS, and nothing only create takes."
  }
  assert {
    condition = (
      [for e in kubernetes_deployment_v1.driver[0].spec[0].template[0].spec[0].container[0].env : e.value if e.name == "DC_PLAYGROUND_CREATE_ARGS"][0]
      == "--ingress http://ingress.example:8081 --mqtt-broker tcp://broker.example:1883"
    )
    error_message = "The create-only arguments should reach the driver apart from the shared ones."
  }
}
