---
section: whats-new
---

## en

- **The install terminal UI is complete for the cluster install, still opt-in.** With `--ui=tui`, `dcctl install local` now runs the whole cluster journey on screen: splash, welcome, preflight, cluster choices, review, progress, and a result screen. A failed install names the stage, the cause, what may have changed, whether the cluster's lock was released, a command to inspect the cluster and the command to re-run once the cause is fixed; nothing is rolled back, and dcctl still exits 1. A successful one shows the `dcctl bootstrap` command to run next. `?` or F1 opens help on the result screen. Every screen is readable with `NO_COLOR` and on a 16-colour or ASCII-only terminal, and it runs on the Windows console (Windows Terminal, PowerShell or `cmd`) with the console restored on exit. The default is unchanged: without `--ui`, or with `--ui=plain`, which is also the path for screen readers, `dcctl install` prints, prompts and exits exactly as before. See [Install terminal UI](./install-terminal-ui.md).

## es

- **La interfaz de terminal de la instalación está completa para la instalación del clúster, y sigue siendo opcional.** Con `--ui=tui`, `dcctl install local` ejecuta ahora en pantalla todo el recorrido del clúster: pantalla inicial, bienvenida, comprobación previa, opciones del clúster, revisión, progreso y una pantalla de resultado. Una instalación fallida indica la etapa, la causa, lo que puede haber cambiado, si se liberó el bloqueo del clúster, un comando para inspeccionar el clúster y el comando para volver a ejecutarla una vez corregida la causa; no se deshace nada, y dcctl sigue saliendo con 1. Una instalación correcta muestra el comando `dcctl bootstrap` que se debe ejecutar a continuación. `?` o F1 abren la ayuda en la pantalla de resultado. Todas las pantallas se leen con `NO_COLOR` y en un terminal de 16 colores o solo ASCII, y funciona en la consola de Windows (Windows Terminal, PowerShell o `cmd`), que se restaura al salir. El valor predeterminado no cambia: sin `--ui`, o con `--ui=plain`, que también es la opción para lectores de pantalla, `dcctl install` imprime, pregunta y sale exactamente igual que antes. Consulta [Interfaz de terminal de la instalación](./install-terminal-ui.md).

## zh-CN

- **安装终端界面已完整覆盖集群安装，并且仍然是可选的。** 使用 `--ui=tui` 时，`dcctl install local` 现在会在屏幕上完成整个集群流程：启动画面、欢迎、预检、集群选项、审核、进度以及结果屏幕。安装失败时会指出失败的阶段、原因、可能已更改的内容、集群锁是否已释放、用于检查集群的命令，以及修复原因后重新运行的命令；不会回滚任何内容，dcctl 仍以 1 退出。安装成功时会显示接下来要运行的 `dcctl bootstrap` 命令。在结果屏幕上按 `?` 或 F1 会打开帮助。每个屏幕在设置 `NO_COLOR` 时以及在 16 色或仅 ASCII 的终端上都可读，并且可在 Windows 控制台（Windows Terminal、PowerShell 或 `cmd`）上运行，退出时会恢复控制台。默认行为不变：不加 `--ui`，或使用 `--ui=plain`（这也是屏幕阅读器用户应使用的方式）时，`dcctl install` 的输出、提示和退出方式与以前完全相同。参见[安装终端界面](./install-terminal-ui.md)。
