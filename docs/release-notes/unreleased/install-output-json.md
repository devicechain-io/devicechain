---
section: whats-new
---

## en

- **`dcctl install --output json`.** `dcctl install` takes a new `-o, --output` flag. `--output json` reports the install as an event stream for automation: one JSON object per line on stdout and nothing else there, covering each stage as it starts, progresses, finishes or fails, the preflight report, the install's own output as log lines, and a final summary line with the verdict. The stream never reads stdin: `--yes` gives the one confirmation an install asks, and without it a run that needs that confirmation is refused before it changes anything. Secret values are never written to it. The exit status and the failure message on stderr are unchanged. The default, `--output text`, prints exactly what it printed before. The events are described in the [install event stream reference](../reference/install-events.md).

## es

- **`dcctl install --output json`.** `dcctl install` admite un nuevo indicador `-o, --output`. `--output json` informa de la instalación como un flujo de eventos para la automatización: un objeto JSON por línea en stdout y nada más allí, que cubre cada etapa cuando empieza, avanza, termina o falla, el informe de comprobaciones previas, la salida de la propia instalación como líneas de registro y una línea final de resumen con el veredicto. El flujo nunca lee stdin: `--yes` da la única confirmación que pide una instalación y, sin él, una ejecución que necesita esa confirmación se rechaza antes de cambiar nada. Nunca se escriben valores secretos en él. El código de salida y el mensaje de error en stderr no cambian. El valor predeterminado, `--output text`, imprime exactamente lo mismo que antes. Los eventos se describen en la [referencia del flujo de eventos de la instalación](../reference/install-events.md).

## zh-CN

- **`dcctl install --output json`。** `dcctl install` 新增 `-o, --output` 标志。`--output json` 以面向自动化的事件流报告安装过程：在 stdout 上每行一个 JSON 对象，且 stdout 上没有其他内容；内容涵盖每个阶段的开始、进展、完成或失败，预检报告，以日志行形式呈现的安装自身输出，以及带有结论的最后一行总结。事件流从不读取 stdin：`--yes` 提供安装唯一会提出的确认；没有它时，需要该确认的运行会在做出任何改动之前被拒绝。机密值从不写入事件流。退出状态和 stderr 上的失败信息保持不变。默认的 `--output text` 打印的内容与以前完全相同。事件说明见[安装事件流参考](../reference/install-events.md)。
