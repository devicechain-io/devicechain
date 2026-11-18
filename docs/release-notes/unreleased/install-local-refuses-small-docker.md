---
section: before-upgrade
---

## en

- **Local installs refuse undersized Docker engines.** A non-compact `dcctl install local` that creates or reuses a local kind cluster now refuses to proceed when Docker reports fewer than 4 CPUs or less than 7.5 GiB of memory. Between 7.5 GiB and 8 GiB it still only warns, so a Docker Desktop set to 8 GB, which reports slightly less, is not refused. Operators who previously continued past the warning with a smaller engine must enlarge Docker or pass `--compact` (or `--size compact`) for the small-footprint preset. Compact installs, `dcctl preflight` and `dcctl bootstrap` keep the warnings.

## es

- **Las instalaciones locales rechazan motores Docker insuficientes.** Una ejecución no compacta de `dcctl install local` que crea o reutiliza un clúster kind local ahora se detiene si Docker indica menos de 4 CPU o menos de 7,5 GiB de memoria. Entre 7,5 GiB y 8 GiB solo muestra una advertencia, de modo que un Docker Desktop configurado con 8 GB, que informa algo menos, no se rechaza. Quienes antes continuaban tras la advertencia con un motor más pequeño deben ampliar los recursos de Docker o usar `--compact` (o `--size compact`) para el preajuste de menor consumo. Las instalaciones compactas, `dcctl preflight` y `dcctl bootstrap` mantienen las advertencias.

## zh-CN

- **本地安装拒绝资源不足的 Docker 引擎。** 创建或复用本地 kind 集群的非紧凑模式 `dcctl install local`，现在会在 Docker 报告少于 4 个 CPU 或不足 7.5 GiB 内存时拒绝继续。内存介于 7.5 GiB 与 8 GiB 之间时仍仅发出警告，因此设置为 8 GB 的 Docker Desktop（报告值略低）不会被拒绝。此前在较小引擎上忽略警告继续安装的运维人员必须增加 Docker 资源，或使用 `--compact`（或 `--size compact`）选择低资源占用预设。紧凑模式安装、`dcctl preflight` 和 `dcctl bootstrap` 仍仅发出警告。
