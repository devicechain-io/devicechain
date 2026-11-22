---
section: whats-new
---

## en

- **More `dcctl` commands can print JSON for scripts.** `dcctl dead-letters list`, `dcctl tenant-deletion status` and `dcctl instances list` accept `-o json` (the same flag `dcctl events failed` has), and `dcctl ha verify` and `dcctl ha verify-db` accept `--json` (like `dcctl ha leaders`). With either flag, stdout carries only the JSON, and an empty `instances list` prints `[]` instead of a sentence. Exit statuses are unchanged: `tenant-deletion status` still exits non-zero unless the target is reached, and `--expect-fail` still inverts the `ha` checks, with its negative-control message on stderr in JSON mode.

## es

- **Más comandos de `dcctl` pueden imprimir JSON para scripts.** `dcctl dead-letters list`, `dcctl tenant-deletion status` y `dcctl instances list` aceptan `-o json` (la misma opción de `dcctl events failed`), y `dcctl ha verify` y `dcctl ha verify-db` aceptan `--json` (como `dcctl ha leaders`). Con cualquiera de las dos, la salida estándar contiene solo el JSON, y un `instances list` vacío imprime `[]` en lugar de una frase. Los códigos de salida no cambian: `tenant-deletion status` sigue terminando con error salvo que se alcance el objetivo, y `--expect-fail` sigue invirtiendo las comprobaciones de `ha`, con su mensaje de control negativo en stderr en modo JSON.

## zh-CN

- **更多 `dcctl` 命令可输出 JSON 供脚本使用。** `dcctl dead-letters list`、`dcctl tenant-deletion status` 和 `dcctl instances list` 支持 `-o json`（与 `dcctl events failed` 的标志相同），`dcctl ha verify` 和 `dcctl ha verify-db` 支持 `--json`（与 `dcctl ha leaders` 相同）。使用这些标志时，标准输出只包含 JSON，没有实例时 `instances list` 输出 `[]` 而不是一句话。退出状态不变：除非达到目标，`tenant-deletion status` 仍以非零状态退出；`--expect-fail` 仍会反转 `ha` 检查的结果，其负向对照消息在 JSON 模式下输出到 stderr。
