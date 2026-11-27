---
section: whats-new
---

## en

- **`dcctl setup local` (Preview): a guided install, then bootstrap, with two approvals.** One terminal session prepares a cluster and creates a first instance on it. The cluster stage is the install's own screens and review; once the cluster is installed, a hand-over screen leads to the instance and root-key escrow choices and the instance's own review. Nothing of the instance runs without its own approval, and no key typed before a review has settled approves it. The escrow passphrase is asked for on screen and never shown. If you leave, or the instance fails, after the cluster stage, the cluster stays installed and setup prints the exact `dcctl bootstrap` command that creates the instance. It is guided only: without an interactive terminal it changes nothing and exits 2, and it has no `--yes`. Its exit status says what it left behind: 0 both done, 1 the cluster stage failed, 2 nothing changed, 3 the cluster installed and no instance created, 4 the cluster installed and the instance failed. `dcctl install` and `dcctl bootstrap` are unchanged. See [Install terminal UI](./install-terminal-ui.md#setup-local).

## es

- **`dcctl setup local` (Preview): una instalación guiada seguida del bootstrap, con dos aprobaciones.** Una sola sesión de terminal prepara un clúster y crea en él una primera instancia. La etapa del clúster son las pantallas y la revisión propias de la instalación; una vez instalado el clúster, una pantalla de traspaso lleva a las opciones de la instancia y del depósito de la clave raíz y a la revisión propia de la instancia. Nada de la instancia se ejecuta sin su propia aprobación, y ninguna tecla escrita antes de que una revisión se haya asentado la aprueba. La frase de paso del depósito se pide en pantalla y nunca se muestra. Si sales, o la instancia falla, después de la etapa del clúster, el clúster sigue instalado y setup imprime el comando `dcctl bootstrap` exacto que crea la instancia. Es solo guiado: sin un terminal interactivo no cambia nada y sale con 2, y no tiene `--yes`. Su código de salida indica lo que deja: 0 ambas cosas hechas, 1 la etapa del clúster falló, 2 no se cambió nada, 3 el clúster instalado y ninguna instancia creada, 4 el clúster instalado y la instancia falló. `dcctl install` y `dcctl bootstrap` no cambian. Consulta [Interfaz de terminal de la instalación](./install-terminal-ui.md#setup-local).

## zh-CN

- **`dcctl setup local`（Preview）：先引导安装，再执行 bootstrap，共两次批准。** 一次终端会话即可准备集群并在其上创建第一个实例。集群阶段就是安装自身的屏幕和审核；集群安装完成后，交接屏幕会进入实例和根密钥托管的选项以及实例自己的审核。实例的任何部分都不会在没有其自身批准的情况下运行，在审核屏幕稳定之前输入的按键也不会批准它。托管口令在屏幕上询问，从不显示。如果在集群阶段之后退出或实例创建失败，集群仍保持已安装状态，setup 会打印创建该实例的确切 `dcctl bootstrap` 命令。它只能以引导方式使用：没有交互式终端时不做任何更改并以 2 退出，也没有 `--yes`。它的退出状态说明留下了什么：0 两者均完成，1 集群阶段失败，2 未做任何更改，3 集群已安装但未创建实例，4 集群已安装但实例创建失败。`dcctl install` 和 `dcctl bootstrap` 不受影响。参见[安装终端界面](./install-terminal-ui.md#setup-local)。
