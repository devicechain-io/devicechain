---
section: before-upgrade
---

## en

- **`dcctl upgrade` refuses to skip a minor release.** Skipping a minor has never been a supported path; `dcctl upgrade` now enforces it. It reads the version the instance's workloads actually run (from the running images, not the declared version, so an upgrade that stopped half-way is judged from what still runs) and refuses a target more than one minor ahead, telling you which minor to upgrade to first. Across a major it allows only the major's first minor. If the running version cannot be read, the upgrade is refused too. `--allow-skipped-minor` overrides the refusal; that path is unsupported and untested, and the flag never disables a protection the database enforces.

## es

- **`dcctl upgrade` rechaza saltarse una versión menor.** Saltarse una versión menor nunca ha sido una ruta con soporte; ahora `dcctl upgrade` lo exige. Lee la versión que ejecutan realmente las cargas de trabajo de la instancia (de las imágenes en ejecución, no de la versión declarada, de modo que una actualización que se detuvo a medias se juzga por lo que sigue ejecutándose) y rechaza un destino más de una versión menor por delante, indicando a qué versión menor actualizar primero. Al cruzar una versión mayor solo permite su primera versión menor. Si no se puede leer la versión en ejecución, la actualización también se rechaza. `--allow-skipped-minor` anula el rechazo; esa ruta no tiene soporte ni está probada, y la opción nunca desactiva una protección que aplique la base de datos.

## zh-CN

- **`dcctl upgrade` 拒绝跳过次版本。** 跳过次版本从来都不是受支持的路径；现在 `dcctl upgrade` 会强制执行这一点。它读取实例工作负载实际运行的版本（从运行中的镜像读取，而不是声明的版本，因此中途停止的升级会按照仍在运行的版本来判断），并拒绝比该版本高出不止一个次版本的目标，同时告诉你应先升级到哪个次版本。跨越主版本时，只允许升级到该主版本的第一个次版本。如果无法读取正在运行的版本，升级同样会被拒绝。`--allow-skipped-minor` 可以覆盖此拒绝；该路径不受支持且未经测试，并且该选项绝不会禁用数据库强制执行的任何保护。
