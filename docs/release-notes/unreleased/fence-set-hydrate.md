---
section: whats-new
---

## en

During startup, event-processing now hydrates the current fence-set version and up to three
preceding versions from device-management before replay. Recent historical events can use their
stamped geometry without waiting for a fence fact or preview. Older versions and failed archive
reads remain unresolved and produce counted containment errors; this does not preload all history.
Startup reconciliation retries missing versions and skips versions already held.

Upgrade in place with the usual rolling procedure. No schema, configuration, or public API change
is required. Old pods keep their existing current-version startup behavior until replaced.

## es

Al iniciar, event-processing carga desde device-management la versión actual del conjunto de
geocercas y hasta tres versiones anteriores antes de reproducir los eventos. Los eventos históricos
recientes pueden usar la geometría de su versión sin esperar un hecho de geocercas ni una vista
previa. Las versiones más antiguas y las lecturas fallidas siguen sin resolverse y generan errores
de contención contabilizados; no se precarga todo el historial. La reconciliación de inicio
reintenta las versiones ausentes y omite las que ya están en memoria.

Actualiza in situ con el procedimiento gradual habitual. No requiere cambios de esquema,
configuración ni API pública. Los pods antiguos mantienen su comportamiento de inicio, que solo
carga la versión actual, hasta que se sustituyen.

## zh-CN

event-processing 启动时会在重放事件之前，从 device-management 加载当前围栏集版本及最多三个先前版本。
近期历史事件可以使用其版本戳对应的几何数据，无需等待围栏事实消息或预览。
更早的版本和归档读取失败的版本仍保持不可解析，并产生计数的包含关系求值错误；此功能不会预加载全部历史。
启动协调会重试缺失版本，并跳过已加载的版本。

按常规滚动升级流程原地升级即可，无需更改数据库结构、配置或公共 API。
旧 pod 在被替换之前仍保持启动时仅加载当前版本的行为。
