---
section: whats-new
---

## en

- **A terminal UI for `dcctl install` (opt-in).** `dcctl install local --ui=tui` runs the install in a full-screen terminal UI: the install's stages with their state, the kind-cluster question answered on screen, and the end result. The first Ctrl+C stops the install safely and the screen stays until it has stopped; a second Ctrl+C exits at once. With `--yes` or `--dev` it shows progress only and exits when the install ends. `--no-splash` skips the logo shown first. The terminal UI is opt-in: without `--ui`, `dcctl install` prints, prompts and exits exactly as before, and `--ui=tui` falls back to that plain output whenever stdin, stdout or stderr is not an interactive terminal, under CI, with `TERM=dumb`, and for `--dry-run`. `--ui=auto` chooses the terminal UI only on an interactive terminal, and never with `--yes` or `--dev`.

## es

- **Una interfaz de terminal para `dcctl install` (opcional).** `dcctl install local --ui=tui` ejecuta la instalación en una interfaz de terminal a pantalla completa: las etapas de la instalación con su estado, la pregunta sobre el clúster kind respondida en pantalla y el resultado final. El primer Ctrl+C detiene la instalación de forma segura y la pantalla permanece hasta que se ha detenido; un segundo Ctrl+C sale de inmediato. Con `--yes` o `--dev` solo muestra el progreso y sale cuando termina la instalación. `--no-splash` omite el logotipo que se muestra al principio. La interfaz de terminal es opcional: sin `--ui`, `dcctl install` imprime, pregunta y sale exactamente igual que antes, y `--ui=tui` vuelve a esa salida de texto siempre que stdin, stdout o stderr no sea un terminal interactivo, en CI, con `TERM=dumb` y con `--dry-run`. `--ui=auto` elige la interfaz de terminal solo en un terminal interactivo, y nunca con `--yes` ni `--dev`.

## zh-CN

- **`dcctl install` 的终端界面（可选）。** `dcctl install local --ui=tui` 在全屏终端界面中运行安装：显示安装的各个阶段及其状态，在屏幕上回答是否创建 kind 集群的问题，并显示最终结果。第一次按 Ctrl+C 会安全地停止安装，界面会一直保留到安装停止；第二次按 Ctrl+C 会立即退出。使用 `--yes` 或 `--dev` 时只显示进度，安装结束后自动退出。`--no-splash` 跳过开始时显示的徽标。终端界面是可选的：不加 `--ui` 时，`dcctl install` 的输出、提示和退出状态与以前完全相同；只要 stdin、stdout 或 stderr 不是交互式终端、在 CI 中、`TERM=dumb` 或使用 `--dry-run`，`--ui=tui` 都会回退到这种纯文本输出。`--ui=auto` 只在交互式终端上选择终端界面，并且在使用 `--yes` 或 `--dev` 时从不选择。
