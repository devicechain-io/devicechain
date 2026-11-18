---
section: whats-new
---

## en

- **A refused `dcctl bootstrap` leaves the local instance record as it found it.** When bootstrap refuses to run over an instance that is already running, `~/.devicechain/instances/<instance>/instance.json` is restored byte for byte, including its `createdAt`. Before, the refused run rewrote it with its own creation time and dcctl version.

## es

- **Un `dcctl bootstrap` rechazado deja el registro local de la instancia tal como estaba.** Cuando bootstrap se niega a ejecutarse sobre una instancia que ya está en marcha, `~/.devicechain/instances/<instance>/instance.json` se restaura byte a byte, incluido su `createdAt`. Antes, la ejecución rechazada lo reescribía con su propia hora de creación y su versión de dcctl.

## zh-CN

- **被拒绝的 `dcctl bootstrap` 会保持本地实例记录不变。** 当 bootstrap 拒绝在已运行的实例上执行时，`~/.devicechain/instances/<instance>/instance.json` 会逐字节恢复，包括其 `createdAt`。此前，被拒绝的运行会用自己的创建时间和 dcctl 版本重写该文件。
