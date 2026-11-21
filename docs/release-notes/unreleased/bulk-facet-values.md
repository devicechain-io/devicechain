---
section: whats-new
---

## en

- **Import facet values in batches.** The new `importFacetValues` GraphQL mutation accepts up to 1,000 values on existing devices, assets, areas, and customers addressed by token. It validates each value against the facet's declared type, writes at SHARED scope, and reports every row, including failures. Preview with `dryRun: true`; rerunning unchanged values reports `EXISTS` without writing again. CSV device import remains available through the existing `importDevices` mutation.

## es

- **Importación de valores de facetas por lotes.** La nueva mutación GraphQL `importFacetValues` acepta hasta 1.000 valores en dispositivos, activos, áreas y clientes existentes identificados por token. Valida cada valor según el tipo declarado de la faceta, escribe en el ámbito SHARED e informa del resultado de cada fila, incluidos los errores. Use `dryRun: true` para obtener una vista previa; al repetir valores sin cambios, devuelve `EXISTS` sin volver a escribirlos. La importación de dispositivos desde CSV sigue disponible mediante la mutación existente `importDevices`.

## zh-CN

- **批量导入分面值。** 新增的 GraphQL 变更 `importFacetValues` 支持通过令牌定位现有设备、资产、区域和客户，每次最多导入 1,000 个值。它按分面声明的类型验证每个值，写入 SHARED 作用域，并报告每一行的结果，包括失败原因。使用 `dryRun: true` 预览；重复提交未更改的值时返回 `EXISTS`，不会再次写入。设备 CSV 导入仍可使用现有的 `importDevices` 变更。
