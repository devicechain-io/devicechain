---
section: before-upgrade
---

## en

- **Local installs refuse undersized Docker engines.** A non-compact `dcctl install local` that creates or reuses a local kind cluster now refuses to proceed when Docker reports fewer than 4 CPUs or less than 8 GiB of memory. Operators who previously continued past the warning must enlarge Docker or pass `--compact` (or `--size compact`) for the small-footprint preset. Compact installs, `dcctl preflight` and `dcctl bootstrap` retain warnings.

## es

- **Las instalaciones locales rechazan motores Docker insuficientes.** Una ejecución no compacta de `dcctl install local` que crea o reutiliza un clúster kind local ahora se detiene si Docker indica menos de 4 CPU o menos de 8 GiB de memoria. Quienes antes continuaban tras la advertencia deben ampliar los recursos de Docker o usar `--compact` (o `--size compact`) para el preajuste de menor consumo. Las instalaciones compactas, `dcctl preflight` y `dcctl bootstrap` mantienen las advertencias.

## zh-CN

- **本地安装拒绝资源不足的 Docker 引擎。** 创建或复用本地 kind 集群的非紧凑模式 `dcctl install local`，现在会在 Docker 报告少于 4 个 CPU 或不足 8 GiB 内存时拒绝继续。此前忽略警告继续安装的运维人员必须增加 Docker 资源，或使用 `--compact`（或 `--size compact`）选择低资源占用预设。紧凑模式安装、`dcctl preflight` 和 `dcctl bootstrap` 仍仅发出警告。
