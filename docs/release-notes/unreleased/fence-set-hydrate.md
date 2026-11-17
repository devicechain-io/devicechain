---
section: whats-new
---

## en

- **A restart loads recent fence-set versions before replay.** During startup, event-processing now loads the current fence-set version and up to three preceding versions from device-management before replay, so recent historical events can use their stamped geometry without waiting for a fence fact or preview. Older versions and failed archive reads remain unresolved and produce counted containment errors; this does not preload all history. Startup reconciliation retries missing versions and skips versions already held. While old and new pods overlap during the rolling upgrade, an old pod still loads only the current version at startup until it is replaced.

## es

- **Un reinicio carga las versiones recientes del conjunto de geocercas antes de reproducir.** Al iniciar, event-processing carga ahora desde device-management la versión actual del conjunto de geocercas y hasta tres versiones anteriores antes de reproducir los eventos, de modo que los eventos históricos recientes pueden usar la geometría de su versión sin esperar un hecho de geocercas ni una vista previa. Las versiones más antiguas y las lecturas fallidas siguen sin resolverse y generan errores de contención contabilizados; no se precarga todo el historial. La reconciliación de inicio reintenta las versiones ausentes y omite las que ya están en memoria. Mientras conviven pods antiguos y nuevos durante la actualización gradual, un pod antiguo sigue cargando solo la versión actual al iniciar hasta que se sustituye.

## zh-CN

- **重启时会在重放之前加载近期的围栏集版本。** event-processing 启动时现在会在重放事件之前，从 device-management 加载当前围栏集版本及最多三个先前版本，因此近期历史事件可以使用其版本戳对应的几何数据，无需等待围栏事实消息或预览。更早的版本和归档读取失败的版本仍保持不可解析，并产生计数的包含关系求值错误；此功能不会预加载全部历史。启动协调会重试缺失版本，并跳过已加载的版本。在滚动升级期间新旧 Pod 并存时，旧 Pod 在被替换之前启动时仍只加载当前版本。
