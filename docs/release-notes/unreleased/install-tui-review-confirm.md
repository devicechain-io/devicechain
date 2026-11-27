---
section: whats-new
---

## en

- **The install terminal UI asks for your approval before anything is installed.** Continue on the cluster choices of `dcctl install local --ui=tui` now opens a review of the plan as the command line would resolve it: the target and whether it is created or reused, the release and image registry, the footprint and topology, TLS, monitoring, backups, any warnings, and where the cluster's state will be kept. It also shows the equivalent non-interactive `dcctl install` command, which reproduces the same plan with `--yes`; a credential such as an alert webhook appears only as a placeholder, and the command is then marked as a template. The review opens with Back focused, and keys typed before it has settled are ignored, so a second Enter can never approve it. The install starts only when you choose Approve and install, and that approval also answers the question to create the local kind cluster, for that cluster only. Going back and changing anything makes a new plan: its checks run again and it needs a review of its own. `--yes`, `--ui=plain` and runs without a terminal are unchanged.

## es

- **La interfaz de terminal de la instalación pide su aprobación antes de instalar nada.** Continue en las opciones del clúster de `dcctl install local --ui=tui` abre ahora una revisión del plan tal como lo resolvería la línea de comandos: el destino y si se crea o se reutiliza, la versión y el registro de imágenes, el tamaño y la topología, TLS, la monitorización, las copias de seguridad, las advertencias y dónde se guardará el estado del clúster. También muestra el comando `dcctl install` equivalente no interactivo, que reproduce el mismo plan con `--yes`; una credencial como un webhook de alertas solo aparece como un marcador, y el comando se marca entonces como plantilla. La revisión se abre con Back seleccionado, y las teclas pulsadas antes de que se haya estabilizado se ignoran, de modo que un segundo Enter nunca puede aprobarla. La instalación solo empieza cuando elige Approve and install, y esa aprobación también responde a la pregunta de crear el clúster kind local, solo para ese clúster. Volver atrás y cambiar cualquier cosa crea un plan nuevo: sus comprobaciones se ejecutan de nuevo y necesita su propia revisión. `--yes`, `--ui=plain` y las ejecuciones sin terminal no cambian.

## zh-CN

- **安装的终端界面会在安装任何内容之前请求您的批准。** 在 `dcctl install local --ui=tui` 的集群选项中选择 Continue，现在会打开一个计划审核页面，按命令行的解析方式展示计划：目标及其是新建还是复用、版本和镜像仓库、规模和拓扑、TLS、监控、备份、各项警告，以及集群状态的保存位置。页面还会显示等效的非交互式 `dcctl install` 命令，它使用 `--yes` 重现同一计划；告警 webhook 等凭据只以占位符显示，此时该命令会被标记为模板。审核页面打开时焦点位于 Back，页面稳定之前按下的键会被忽略，因此多按一次 Enter 永远不会批准它。只有选择 Approve and install 后安装才会开始，该批准同时会回答是否创建本地 kind 集群的问题，且仅针对该集群。返回并更改任何内容都会生成新的计划：其检查会重新运行，并需要单独审核。`--yes`、`--ui=plain` 以及没有终端的运行均保持不变。
