---
section: whats-new
---

## en

- **The console and MCP keep working while an upgrade is still rolling.** While some pods still run the previous release, saving a detection rule, validating a rule on a profile with a STRING metric, and reading a device's latest readings no longer fail when the request reaches an older pod. The console sends the older request instead, and shows a text reading the older pod cannot serve as "Text reading shown after the upgrade completes". A rule drafted with Describe is not saved by an older pod, because that pod cannot record that the rule was drafted with Describe. The console asks you to save it again after the upgrade completes. The MCP `get_latest_measurements` tool returns numeric readings from an older device-state pod and sets `textUnavailable` when text readings could not be read.

## es

- **La consola y MCP siguen funcionando mientras una actualización aún está en curso.** Mientras algunos pods ejecutan todavía la versión anterior, guardar una regla de detección, validar una regla en un perfil con una métrica STRING y leer las últimas lecturas de un dispositivo ya no fallan cuando la solicitud llega a un pod antiguo. La consola envía en su lugar la solicitud antigua y muestra una lectura de texto que el pod antiguo no puede servir como «La lectura de texto se mostrará cuando termine la actualización». Un pod antiguo no guarda una regla redactada con Describir, porque no puede registrar que la regla se redactó con Describir. La consola le pide que la guarde de nuevo cuando termine la actualización. La herramienta MCP `get_latest_measurements` devuelve las lecturas numéricas de un pod antiguo de device-state y establece `textUnavailable` cuando no se pudieron leer las lecturas de texto.

## zh-CN

- **升级仍在滚动进行时，控制台和 MCP 可继续正常工作。** 当部分 Pod 仍在运行上一版本时，保存检测规则、在带有 STRING 指标的配置文件上验证规则以及读取设备的最新读数，在请求到达旧 Pod 时不再失败。控制台会改为发送旧版请求，并将旧 Pod 无法提供的文本读数显示为“升级完成后显示文本读数”。旧 Pod 不会保存用“描述”起草的规则，因为它无法记录该规则是用“描述”起草的。控制台会提示你在升级完成后再次保存。MCP 工具 `get_latest_measurements` 会返回旧版 device-state Pod 的数值读数，并在无法读取文本读数时设置 `textUnavailable`。
