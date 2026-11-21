---
section: whats-new
---

## en

- **Import facet values from a CSV file in the console.** On the Facets screen, **Import values** reads a CSV file of up to 10,000 rows and 5 MiB, maps its columns (entity type, entity token, facet key, value), previews every row with a dry run, then sets the values in batches of at most 1,000. A row that repeats an earlier entity and facet key anywhere in the file is held back and reported. The results can be filtered and downloaded, rows that were refused can be retried on their own, and the failed-rows file keeps only the four import columns, so a credential column in your source file is never written back out.

## es

- **Importa valores de facetas desde un archivo CSV en la consola.** En la pantalla Facetas, **Importar valores** lee un archivo CSV de hasta 10.000 filas y 5 MiB, asigna sus columnas (tipo de entidad, token de la entidad, clave de faceta, valor), muestra una vista previa de cada fila con una ejecución de prueba y luego asigna los valores en lotes de 1.000 como máximo. Una fila que repite una entidad y una clave de faceta anteriores en cualquier punto del archivo no se envía y se informa. Los resultados se pueden filtrar y descargar, las filas rechazadas se pueden reintentar por separado y el archivo de filas fallidas conserva solo las cuatro columnas de importación, de modo que una columna de credenciales del archivo de origen nunca se vuelve a escribir.

## zh-CN

- **在控制台中通过 CSV 文件导入分面值。** 在“分面”页面中，**导入值**可读取最多 10,000 行、5 MiB 的 CSV 文件，映射其列（实体类型、实体令牌、分面键、值），先以试运行预览每一行，再按每批最多 1,000 个值写入。文件中任何位置重复出现的实体和分面键组合只发送第一行，其余行会被拦截并报告。结果可筛选和下载，被拒绝的行可单独重试；失败行文件只保留四个导入列，因此源文件中的凭据列不会被再次写出。
