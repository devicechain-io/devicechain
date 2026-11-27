---
section: whats-new
---

## en

- **The install terminal UI lets you change the cluster choices before anything runs.** After the preflight, `dcctl install local --ui=tui` shows the cluster choices: the target (a managed local kind cluster, which you can rename, or an existing context picked from the names in your kubeconfig), the footprint (Standard or Compact, with what Compact leaves out spelled out under it) and the topology (Single or HA), each marked with the flag that set it. Choose another context is also on the preflight screen. Advanced opens a drawer with editors for the monitoring stack, the bundled CloudNativePG operator and, on Compact, TLS, and says whether database backups run, as the plan decides it; every other flag you gave on the command line is listed there by name and carried into the install unchanged, and credentials show only as "configured (hidden)". Each change is checked the way the command line would check it, so a combination the command line refuses is refused on screen too, and the read-only checks run again for the changed plan: Continue stays unavailable until they pass.

## es

- **La interfaz de terminal de la instalación permite cambiar las opciones del clúster antes de que se ejecute nada.** Tras la comprobación previa, `dcctl install local --ui=tui` muestra las opciones del clúster: el destino (un clúster kind local gestionado, al que se puede cambiar el nombre, o un contexto existente elegido entre los nombres de su kubeconfig), el tamaño (Standard o Compact, con lo que Compact deja fuera explicado debajo) y la topología (Single o HA), cada una marcada con la opción de línea de comandos que la estableció. Choose another context también está en la pantalla de comprobación previa. Advanced abre un panel con editores para la pila de monitorización, el operador CloudNativePG incluido y, con Compact, TLS, e indica si se harán copias de seguridad de la base de datos, tal como lo decide el plan; cualquier otra opción indicada en la línea de comandos aparece ahí por su nombre y se aplica a la instalación sin cambios, y las credenciales solo se muestran como "configured (hidden)". Cada cambio se valida igual que lo haría la línea de comandos, de modo que una combinación que la línea de comandos rechaza también se rechaza en pantalla, y las comprobaciones de solo lectura vuelven a ejecutarse para el plan modificado: Continue no está disponible hasta que pasan.

## zh-CN

- **安装的终端界面允许在任何操作开始之前更改集群选项。** 预检之后，`dcctl install local --ui=tui` 会显示集群选项：目标（可重命名的托管本地 kind 集群，或从 kubeconfig 中的名称里选择的现有上下文）、规模（Standard 或 Compact，并在下方说明 Compact 会省略哪些内容）以及拓扑（Single 或 HA），每一项都标明是由哪个命令行参数设置的。预检屏幕上也提供 Choose another context。Advanced 会打开一个面板，其中可编辑监控组件、内置的 CloudNativePG operator，以及在 Compact 下的 TLS，并按计划的实际结果显示是否进行数据库备份；命令行中给出的其他所有参数都会按名称列在其中，并原样带入安装，凭据只显示为 "configured (hidden)"。每项更改都按命令行的方式进行校验，因此命令行会拒绝的组合在屏幕上同样会被拒绝，并且会针对更改后的计划重新运行只读检查：在检查通过之前，Continue 始终不可用。
