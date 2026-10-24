# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# The kube-prometheus-stack values the platform's own behaviour rests on.
#
# A key the chart renames or drops does not fail a `helm install`: the value is
# silently ignored and the install comes up healthy without the behaviour. So the
# keys below were each checked against the PINNED chart's rendered output (the
# dashboard sidecar's FOLDER_ANNOTATION, NAMESPACE and LABEL environment, the
# provider's foldersFromFilesStructure, the admin password's Secret reference, the
# Service type, the CRD upgrade job), and this pins that the module still hands the
# chart those keys. The rendering check is the one to repeat on a chart bump:
#   helm template x prometheus-community/kube-prometheus-stack --version <pin> -f <values>
#
# Every check decodes the document OUTSIDE try(), so a wrong address fails the run
# rather than reading as "absent". Runs against mocked providers, no cluster needed.
# Run it with hack/check-tofu-module-tests.sh.

mock_provider "helm" {}

run "dashboards_are_filed_per_instance_folder" {
  command = plan

  assert {
    condition     = yamldecode(helm_release.kube_prometheus_stack.values[0]).grafana.sidecar.dashboards.folderAnnotation == "grafana_folder"
    error_message = "sidecar.dashboards.folderAnnotation must be grafana_folder: without it the sidecar ignores each instance's folder annotation"
  }

  assert {
    condition     = yamldecode(helm_release.kube_prometheus_stack.values[0]).grafana.sidecar.dashboards.provider.foldersFromFilesStructure == true
    error_message = "sidecar.dashboards.provider.foldersFromFilesStructure must be true: it is what turns the sidecar's subdirectories into folders"
  }

  assert {
    condition     = yamldecode(helm_release.kube_prometheus_stack.values[0]).grafana.sidecar.dashboards.searchNamespace == "ALL"
    error_message = "sidecar.dashboards.searchNamespace must be ALL: the dashboard ConfigMaps live in each instance's namespace"
  }

  assert {
    condition     = yamldecode(helm_release.kube_prometheus_stack.values[0]).grafana.sidecar.dashboards.label == "grafana_dashboard"
    error_message = "sidecar.dashboards.label must be grafana_dashboard: it is the label the instance chart puts on its dashboard ConfigMaps"
  }
}

run "grafana_login_and_service" {
  command = plan

  assert {
    condition     = yamldecode(helm_release.kube_prometheus_stack.values[0]).grafana.admin.existingSecret == "dc-grafana-admin"
    error_message = "grafana.admin.existingSecret must name the Secret dcctl writes: the password is never in the values"
  }

  assert {
    condition     = yamldecode(helm_release.kube_prometheus_stack.values[0]).grafana.service.type == "ClusterIP"
    error_message = "Grafana must stay a ClusterIP Service: it is reached by port-forward or the operator-tier ingress, never exposed by default"
  }
}

run "instance_monitors_are_selected_from_every_namespace" {
  command = plan

  assert {
    condition     = yamldecode(helm_release.kube_prometheus_stack.values[0]).prometheus.prometheusSpec.serviceMonitorSelectorNilUsesHelmValues == false
    error_message = "serviceMonitorSelectorNilUsesHelmValues must be false: otherwise only ServiceMonitors labelled with this release are scraped and no instance is"
  }

  assert {
    condition     = yamldecode(helm_release.kube_prometheus_stack.values[0]).prometheus.prometheusSpec.ruleSelectorNilUsesHelmValues == false
    error_message = "ruleSelectorNilUsesHelmValues must be false: otherwise the instance's PrometheusRules are never loaded"
  }
}

run "crds_are_upgraded_with_the_chart" {
  command = plan

  assert {
    condition     = yamldecode(helm_release.kube_prometheus_stack.values[0]).crds.enabled == true
    error_message = "crds.enabled must be true: the instance chart's ServiceMonitors and PrometheusRules need the CRDs"
  }

  assert {
    condition     = yamldecode(helm_release.kube_prometheus_stack.values[0]).crds.upgradeJob.enabled == true
    error_message = "crds.upgradeJob.enabled must be true: Helm never upgrades CRDs, so a chart bump would leave an existing cluster's operator on the old schema"
  }

  assert {
    condition     = yamldecode(helm_release.kube_prometheus_stack.values[0]).crds.upgradeJob.image.busybox.tag != "latest"
    error_message = "the CRD upgrade job's busybox image must be pinned: the chart defaults it to latest"
  }
}
