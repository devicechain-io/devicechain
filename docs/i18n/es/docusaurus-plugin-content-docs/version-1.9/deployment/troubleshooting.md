{/* Copyright The DeviceChain Authors
    SPDX-License-Identifier: Apache-2.0 */}

# Solución de problemas y operaciones habituales {#troubleshooting}

Empieza por el síntoma de la tabla. Si ya tienes una alerta activa, consulta su
significado y respuesta en el [manual de alertas](./alert-runbook.md). Los
[tableros de resumen y de etapas de Grafana](./grafana-boards.md) ayudan a localizar
la primera etapa en la que el trabajo deja de avanzar.

## Encuentra tu síntoma {#symptoms}

| Síntoma | Primera comprobación | Procedimiento |
| --- | --- | --- |
| La instalación o el bootstrap se detiene antes de arrancar los servicios | Ejecuta `dcctl preflight`; lee el paso fallido y los eventos de Kubernetes. Otro operador puede tener ocupado el clúster. | [Bootstrap](./bootstrap.md), [bloqueo del clúster](./cluster-lock.md) y [reanudar un bootstrap fallido](./disaster-recovery.md#resuming-a-bootstrap). |
| Los pods están Pending, se reinician o no están Ready | Describe el pod afectado para ver fallos de planificación, imagen, volumen y sondas. Si se reinició, consulta los logs del contenedor anterior antes de sustituirlo. | [Reinicios de contenedores](./observability.md#container-restarts) y [rechazos al arrancar en las notas de actualización](/1.6/deployment/releases-and-upgrades#v160-before-upgrade). |
| Tras una recuperación, user-management rechaza el arranque y los demás servicios nunca están Ready | Lee el error de arranque de user-management. Una clave raíz incorrecta no abre la clave de firma almacenada; reiniciar no lo repara. | [Recuperación de la clave raíz](./disaster-recovery.md#root-key) y [verificar la custodia](./disaster-recovery.md#verify). |
| El inicio de sesión funciona, pero las llamadas a la API devuelven 401 | Comprueba la audiencia del token del cliente y los logs de rechazo del servicio receptor; un token válido puede estar vinculado a otro recurso. | [Comprobaciones de audiencia](./observability.md#token-audience-checks). |
| Un usuario no supera el segundo factor | Comprueba la sincronización de relojes y la auditoría de la cuenta. Si quedó bloqueada tras intentos con contraseña correcta, restablece la contraseña antes que el segundo factor. | [Bloqueos del segundo factor](./observability.md#mfa-alerts). |
| MQTT agota el tiempo de espera o falla la negociación TLS | Comprueba DNS, el nombre del certificado, la conectividad TCP y el estado del listener del Gateway. | [Problemas de MQTT externo](./connecting-from-outside.md#troubleshooting). |
| Un dispositivo se conecta pero falta telemetría | Confirma el tenant y el dispositivo; revisa los motivos de eventos fallidos y las mediciones descartadas en la pestaña Connectivity del dispositivo. | [¿Por qué no aparecen mis datos?](../guides/why-isnt-my-data-showing-up.md) y [causas habituales](../getting-help.md#things-that-silently-swallow-data). |
| La telemetría se retrasa y crece la cola | Localiza el primer consumidor lento en los tableros; distingue mensajes pendientes y en curso y comprueba la base de datos y el broker posteriores. | [Cola del consumidor](./observability.md#consumer-backlog), [contrapresión de ingesta](./observability.md#ingest-backpressure) y [ajustes](./tuning-an-instance.md). |
| Los comandos siguen en cola o retenidos | Inspecciona el estado del comando, la presencia del dispositivo, la ventana de mantenimiento y el canal antes de reintentar. | [Alertas de entrega de comandos](./observability.md#command-delivery-alerts) y [enviar comandos](../guides/sending-commands.md). |
| Una regla no genera una alarma | Comprueba la versión publicada del perfil, la métrica recibida y los errores de reglas y el estado de reproducción del motor. | [Motor de detección](./detection-engine.md) y [probar reglas](../guides/testing-your-rules.md). |
| Existe una alarma pero no llega la notificación | Comprueba la coincidencia de políticas y la configuración del canal; sigue el tablero de notificaciones y sus alertas. | [Canales de notificación](../guides/notification-channels.md) y [tableros de Grafana](./grafana-boards.md). |
| El almacenamiento se llena o una ampliación no llega al PVC | Comprueba el espacio libre y el crecimiento de ambas bases de datos y los volúmenes del broker; compara el tamaño solicitado con el PVC. | [Dimensionar y ampliar el almacén de eventos](./tuning-an-instance.md#event-store-sizing) y [elegir un tamaño](./sizing.md). |
| Las copias dejan de enviarse o se rechaza la recuperación | Comprueba las alertas de backup, la ventana de recuperación de cada almacén y la custodia de la clave raíz; conserva el rechazo exacto. | [Alertas de archivado](./observability.md#backup-archiving) y [recuperación ante desastres](./disaster-recovery.md). |
| Un tenant eliminado sigue en proceso de borrado | Identifica el sistema de almacenamiento que bloquea el progreso; desaparecer de la consola no demuestra que haya terminado. | [Borrado detenido](./tenant-deletion.md#stalled) y [señales de finalización](./tenant-deletion.md#erased-signal). |

## Recoge pruebas antes de cambiar nada {#evidence}

Anota el intervalo y la zona horaria, la instancia, el tenant afectado, la versión
instalada, el último paso correcto y el primero fallido. Selecciona el mismo intervalo
en cada tablero de Grafana. Guarda el nombre y las etiquetas de la alerta, además de su color.

Estos ejemplos de solo lectura usan la instancia `acme` en el namespace `dci-acme`.
Sustituye el namespace y el nombre del pod por los tuyos. Ejecuta los ejemplos en
Bash; en Windows, usa Git Bash o WSL.

```bash
dcctl version
dcctl preflight
kubectl -n dci-acme get pods
kubectl -n dci-acme get events --sort-by=.metadata.creationTimestamp
kubectl -n dci-acme describe pod YOUR_POD
kubectl -n dci-acme logs YOUR_POD --all-containers=true --since=30m
```

Si un contenedor se reinició, solicita también sus logs anteriores; sustituye el
nombre del contenedor por el de la descripción del pod. El comando devuelve un
error si no existe un contenedor anterior.

```bash
kubectl -n dci-acme logs YOUR_POD -c YOUR_CONTAINER --previous
```

Los logs pueden haber rotado; la ausencia de salida no demuestra que no hubiera
fallos. Lee los [logs estructurados](./observability.md#logs) junto con los eventos
de Kubernetes. Conserva las pruebas antes de reiniciar pods, cambiar la configuración o reintentar una escritura.

:::caution Oculta los datos sensibles antes de compartir
Elimina tokens, contraseñas, cadenas de conexión, claves raíz, material de custodia
y nombres de host privados de los logs y la salida. Nunca adjuntes un Secret de
Kubernetes ni un volcado de configuración sin filtrar a un issue público. Consulta
[Cómo obtener ayuda](../getting-help.md) para los canales, incluidos los informes privados de seguridad.
:::

## Operaciones habituales {#routine-operations}

Son momentos de revisión sugeridos, no programaciones automáticas ni nuevos límites.
Elige una frecuencia adecuada al tráfico, los objetivos de recuperación y las ventanas de mantenimiento.

| Cuándo | Revisión y acción | Procedimiento |
| --- | --- | --- |
| Cada día de operación y tras un incidente | Revisa alertas activas, reinicios, colas y espacio libre. Confirma que las alertas llegan a un operador; un panel vacío puede indicar métricas ausentes. | [Tableros de Grafana](./grafana-boards.md) y [enrutamiento de alertas](./observability.md#alert-routing). |
| Cada ciclo de backup | Verifica el archivado de los almacenes principal y de eventos y sus ventanas de recuperación. Comprueba por separado los objetos y streams preservados. | [Dos copias de bases de datos](./disaster-recovery.md#two-tiers), [alertas de backup](./observability.md#backup-archiving) y [otro estado](./disaster-recovery.md#other-state). |
| Tras el bootstrap o cambios de clave, y antes de un simulacro | Verifica la huella de la custodia; guarda el artefacto cifrado y su frase de contraseña por separado y fuera del clúster. | [Verificar la custodia](./disaster-recovery.md#verify). |
| Según tu calendario de simulacros | Ensaya la recuperación en un clúster nuevo con ambos almacenes y la clave raíz original; verifica inicio de sesión, uso de secretos e historial. Verificar solo el backup de streams y objetos no es un simulacro de restauración. | [Recuperar una instancia](./disaster-recovery.md#recover). |
| Antes de actualizar o cambiar la configuración | Lee los rechazos al arrancar y la ruta de actualización admitida, revisa el overlay guardado, comprueba las copias y ejecuta un dry run antes de aplicar. | [Versiones y actualizaciones](./releases-and-upgrades.md), [política de versiones](./versioning-policy.md#upgrade-paths) y [dry run del overlay](./tuning-an-instance.md#dry-run). |
| Después de actualizar o cambiar la configuración | Comprueba que el despliegue está listo, los tableros y la ingesta y el resultado de un comando de un dispositivo representativo; confirma que siguen enviándose las copias. | [Observabilidad](./observability.md) y [enviar comandos](../guides/sending-commands.md). |
| Al revisar capacidad y certificados | Compara crecimiento y retención con la capacidad disponible; revisa las anclas de confianza próximas a caducar y planifica la rotación antes de su vencimiento. | [Dimensionamiento](./sizing.md), [ajustes](./tuning-an-instance.md) y [certificados de dispositivos](./device-certificates.md). |
| Al revisar accesos y dar de baja tenants | Revisa cuentas de servicio y roles; para el borrado, verifica la finalización y realiza la limpieza que corresponde al operador. | [Cuentas de servicio](../user-guide/service-accounts.md) y [borrado de tenants](./tenant-deletion.md#operator-steps). |

Los comandos de streams y objetos son **Preview**: `dcctl backup inspect --verify`
comprueba los bytes enumerados y `dcctl restore --plan` solo planifica. No hay comando
para aplicar la restauración ni checkpoint coordinado de bases de datos. Sigue los
requisitos de pausa y verificación de [Exportar streams y contenido de objetos](./disaster-recovery.md#export-streams-objects).
