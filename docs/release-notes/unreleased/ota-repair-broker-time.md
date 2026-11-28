---
section: whats-new
---

## en

- **A redelivered final update report no longer rolls back a device's installed version.** When the
  report that completed an update was delivered again, its repair of the installed version was
  stamped with the time the service received the report rather than the time the broker recorded
  it, so on a replica whose clock ran ahead it could outrank a newer report and show an older
  version as installed. The repair now carries the completing report's own broker time. The upgrade
  adds one nullable column to the update attempts table; nothing needs doing.

## es

- **Un informe final de actualización reenviado ya no revierte la versión instalada de un
  dispositivo.** Cuando el informe que completó una actualización se entregaba de nuevo, su
  reparación de la versión instalada se marcaba con la hora en que el servicio lo recibía en lugar
  de la hora registrada por el broker, de modo que en una réplica con el reloj adelantado podía
  imponerse a un informe más reciente y mostrar como instalada una versión anterior. La reparación
  usa ahora la hora del broker del propio informe que completó la actualización. La actualización
  añade una columna que admite nulos a la tabla de intentos de actualización; no hay que hacer nada.

## zh-CN

- **重新投递的最终更新报告不再回退设备的已安装版本。** 当完成某次更新的报告被再次投递时，
  它对已安装版本的修复使用的是服务接收报告的时间，而不是代理（broker）记录该报告的时间；
  因此在时钟偏快的副本上，它可能压过更新的报告，把较旧的版本显示为已安装。现在，修复使用
  完成更新的那份报告本身的代理时间。此次升级会在更新尝试表中添加一个可为空的列，无需任何操作。
