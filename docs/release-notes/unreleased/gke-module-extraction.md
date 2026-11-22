---
section: before-upgrade
---

## en

- **The GKE configuration in `deploy/gke` now creates its cluster through a shared module.** The variables, defaults and outputs are unchanged, and a state applied by an earlier checkout moves to the new addresses on its next `tofu apply` (`tofu plan` shows every resource as a move and plans nothing else). Once a state has been applied from this version, do not apply it from an older checkout: the older configuration no longer finds its resources at the old addresses, so it would plan to delete the cluster and create a new one. If you run load tests from several checkouts, update them all first. The README's "If an apply fails part-way" step now names the cluster as `module.gke_cluster.google_container_cluster.this`.

## es

- **La configuración de GKE en `deploy/gke` ahora crea su clúster mediante un módulo compartido.** Las variables, los valores por defecto y las salidas no cambian, y un estado aplicado desde una copia anterior pasa a las nuevas direcciones en el siguiente `tofu apply` (`tofu plan` muestra cada recurso como un movimiento y no planifica nada más). Cuando un estado se haya aplicado desde esta versión, no lo aplique desde una copia anterior: la configuración antigua ya no encuentra sus recursos en las direcciones previas, así que planificaría eliminar el clúster y crear uno nuevo. Si ejecuta pruebas de carga desde varias copias, actualícelas todas primero. El paso «If an apply fails part-way» del README ahora nombra el clúster como `module.gke_cluster.google_container_cluster.this`.

## zh-CN

- **`deploy/gke` 中的 GKE 配置现在通过共享模块创建集群。** 变量、默认值和输出均未改变；由较早检出版本应用过的状态会在下一次 `tofu apply` 时迁移到新地址（`tofu plan` 会把每个资源显示为一次移动，不会规划其他任何更改）。状态一旦由此版本应用，就不要再用较旧的检出版本应用它：旧配置在原地址找不到这些资源，会规划删除集群并创建新集群。如果您从多个检出目录运行负载测试，请先全部更新。README 中“If an apply fails part-way”一节现在把集群写作 `module.gke_cluster.google_container_cluster.this`。
