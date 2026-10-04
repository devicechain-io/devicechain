# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# Which control-plane scrapes reach the kube-prometheus-stack values.
#
# On a cluster whose control plane the provider runs, the chart's Services for the
# scheduler, controller manager, etcd and kube-proxy select no pod, so their bundled
# *Down rules fire for as long as the cluster exists. dcctl lists the components it
# found no pod for; this reads the values document Helm is handed, so a key that
# drifts is a failure here rather than an alert nobody can clear.
#
# Every check decodes the document OUTSIDE try(): a wrong address or a decode error
# must fail the run, not read as "the key is absent". The slim runs are the positive
# control for the "absent" checks: they read the same document and find the keys.
#
# Runs against mocked providers, so no cluster is needed. Run it with
# hack/check-tofu-module-tests.sh, which tests a COPY of the module.

mock_provider "helm" {}

run "default_scrapes_everything" {
  command = plan

  assert {
    condition     = !contains(keys(yamldecode(helm_release.kube_prometheus_stack.values[0])), "kubeControllerManager")
    error_message = "kubeControllerManager: with nothing listed no control-plane scrape may be switched off"
  }

  assert {
    condition     = !contains(keys(yamldecode(helm_release.kube_prometheus_stack.values[0])), "kubeScheduler")
    error_message = "kubeScheduler: with nothing listed no control-plane scrape may be switched off"
  }

  assert {
    condition     = !contains(keys(yamldecode(helm_release.kube_prometheus_stack.values[0])), "kubeEtcd")
    error_message = "kubeEtcd: with nothing listed no control-plane scrape may be switched off"
  }

  assert {
    condition     = !contains(keys(yamldecode(helm_release.kube_prometheus_stack.values[0])), "kubeProxy")
    error_message = "kubeProxy: with nothing listed no control-plane scrape may be switched off"
  }
}

run "managed_control_plane" {
  command = plan

  variables {
    unscraped_control_plane = ["kubeControllerManager", "kubeScheduler", "kubeEtcd"]
  }

  assert {
    condition     = yamldecode(helm_release.kube_prometheus_stack.values[0]).kubeControllerManager.enabled == false
    error_message = "kubeControllerManager: a listed component must have its scrape and Down rules switched off"
  }

  assert {
    condition     = yamldecode(helm_release.kube_prometheus_stack.values[0]).kubeScheduler.enabled == false
    error_message = "kubeScheduler: a listed component must have its scrape and Down rules switched off"
  }

  assert {
    condition     = yamldecode(helm_release.kube_prometheus_stack.values[0]).kubeEtcd.enabled == false
    error_message = "kubeEtcd: a listed component must have its scrape and Down rules switched off"
  }

  assert {
    condition     = !contains(keys(yamldecode(helm_release.kube_prometheus_stack.values[0])), "kubeProxy")
    error_message = "kubeProxy: a component not listed must stay scraped"
  }
}

run "slim_switches_all_four_off" {
  command = plan

  variables {
    slim = true
  }

  assert {
    condition     = yamldecode(helm_release.kube_prometheus_stack.values[0]).kubeControllerManager.enabled == false
    error_message = "kubeControllerManager: slim must switch every control-plane scrape off"
  }

  assert {
    condition     = yamldecode(helm_release.kube_prometheus_stack.values[0]).kubeScheduler.enabled == false
    error_message = "kubeScheduler: slim must switch every control-plane scrape off"
  }

  assert {
    condition     = yamldecode(helm_release.kube_prometheus_stack.values[0]).kubeEtcd.enabled == false
    error_message = "kubeEtcd: slim must switch every control-plane scrape off"
  }

  assert {
    condition     = yamldecode(helm_release.kube_prometheus_stack.values[0]).kubeProxy.enabled == false
    error_message = "kubeProxy: slim must switch every control-plane scrape off"
  }
}

run "slim_ignores_a_partial_list" {
  command = plan

  variables {
    slim                    = true
    unscraped_control_plane = ["kubeProxy"]
  }

  assert {
    condition     = yamldecode(helm_release.kube_prometheus_stack.values[0]).kubeControllerManager.enabled == false
    error_message = "kubeControllerManager: slim must switch every control-plane scrape off whatever is listed"
  }

  assert {
    condition     = yamldecode(helm_release.kube_prometheus_stack.values[0]).kubeScheduler.enabled == false
    error_message = "kubeScheduler: slim must switch every control-plane scrape off whatever is listed"
  }

  assert {
    condition     = yamldecode(helm_release.kube_prometheus_stack.values[0]).kubeEtcd.enabled == false
    error_message = "kubeEtcd: slim must switch every control-plane scrape off whatever is listed"
  }

  assert {
    condition     = yamldecode(helm_release.kube_prometheus_stack.values[0]).kubeProxy.enabled == false
    error_message = "kubeProxy: slim must switch every control-plane scrape off whatever is listed"
  }
}

run "a_repeated_name_is_harmless" {
  command = plan

  variables {
    unscraped_control_plane = ["kubeEtcd", "kubeEtcd"]
  }

  assert {
    condition     = yamldecode(helm_release.kube_prometheus_stack.values[0]).kubeEtcd.enabled == false
    error_message = "kubeEtcd: a repeated name must still switch the component off"
  }

  assert {
    condition     = !contains(keys(yamldecode(helm_release.kube_prometheus_stack.values[0])), "kubeScheduler")
    error_message = "kubeScheduler: only the listed component is switched off"
  }
}

run "an_unknown_component_is_refused" {
  command = plan

  variables {
    unscraped_control_plane = ["kubeApiServer"]
  }

  expect_failures = [var.unscraped_control_plane]
}
