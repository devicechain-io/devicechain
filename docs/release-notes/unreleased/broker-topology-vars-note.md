---
section: whats-new
---

## en

- **`dcctl` says when it overrides a stated broker topology.** `dcctl` always sets `ha` and `nats_cluster_replicas` itself, so the broker's server count and its stream replica count stay together. A value you state for either in `TF_VAR_*` or a tfvars file is not used. `dcctl bootstrap` and `dcctl upgrade` now print a note when you state one, and [Which value wins](./sizing.md#precedence) documents the exception.

## es

- **`dcctl` avisa cuando sustituye una topología del broker que usted ha indicado.** `dcctl` siempre fija `ha` y `nats_cluster_replicas` por sí mismo, de modo que el número de servidores del broker y el número de réplicas de sus streams se mantienen juntos. No se usa el valor que usted indique para cualquiera de ellas en `TF_VAR_*` o en un archivo tfvars. `dcctl bootstrap` y `dcctl upgrade` imprimen ahora una nota cuando usted indica uno, y [Qué valor prevalece](./sizing.md#precedence) documenta la excepción.

## zh-CN

- **`dcctl` 在覆盖你声明的代理拓扑时会给出提示。** `dcctl` 总是自己设置 `ha` 和 `nats_cluster_replicas`，使代理的服务器数量与其流副本数保持一致。你在 `TF_VAR_*` 或 tfvars 文件中为其中任何一个声明的值都不会被使用。现在，当你声明了其中一个时，`dcctl bootstrap` 和 `dcctl upgrade` 会打印一条说明，[哪个值优先](./sizing.md#precedence)也记录了这一例外。
