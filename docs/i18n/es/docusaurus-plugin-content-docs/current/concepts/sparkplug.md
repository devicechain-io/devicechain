---
title: Ingesta de Sparkplug-B
---

# Ingesta de Sparkplug-B

Muchas flotas industriales y de automatización de edificios ya publican telemetría como [Eclipse Sparkplug B](https://sparkplug.eclipse.org/) sobre un broker MQTT que ellas mismas operan. DeviceChain puede ingerir directamente desde esas redes sin pedirles a los dispositivos que cambien nada. Se une a tu entorno Sparkplug como una **Host Application** y traduce el tráfico de borde (edge) a los mismos eventos que produce cualquier otro transporte.

Con MQTT simple, DeviceChain *es* el broker. La ingesta de Sparkplug funciona al revés: DeviceChain **se conecta hacia afuera, a tu broker**, como cliente, se suscribe a los grupos de Sparkplug que configures y sigue el protocolo de sesión de Sparkplug.

## Qué hace

- **Se anuncia como una Host Application.** DeviceChain publica el mensaje `STATE` de Sparkplug para que los nodos de borde sepan que hay un consumidor en línea. Sigue el protocolo de negociación de nacimiento/muerte (birth/death), así que sabe en todo momento qué nodos y dispositivos están activos.

- **Sigue la sesión de Sparkplug.** Sparkplug es un protocolo con estado. Un nodo de borde envía un certificado **BIRTH** que define sus métricas (y alias compactos), luego un flujo de mensajes **DATA** que los referencian, y un **DEATH** cuando queda fuera de línea. DeviceChain ejecuta la máquina de estados de sesión completa: rastrea los alias y la secuencia de mensajes de cada nodo, y detecta un salto o un nacimiento (birth) perdido. Cuando necesita resincronizarse, le pide al nodo que se vuelva a anunciar (un *rebirth*), de modo que un mensaje descartado nunca corrompe silenciosamente lo que decodifica.

- **Asigna identidades de borde a dispositivos.** Cada `{group}/{node}` de Sparkplug (o `{group}/{node}/{device}` para un dispositivo bajo un nodo) se convierte en el [`externalId`](./domain-model.md) de un dispositivo de DeviceChain. Si habilitas el autorregistro para una fuente, se crea un dispositivo automáticamente la primera vez que se lo ve. En caso contrario, las identidades desconocidas se descartan y se contabilizan, así que mantienes el control de qué entra en tu registro. El tráfico de un inquilino que ha sido eliminado también se descarta mientras se recuperan sus datos, y se contabiliza por separado. Así, un operador que vigila los descartes puede distinguir "no está en el registro" de "el inquilino ya no existe".

- **Produce presencia autoritativa.** Un BIRTH de nodo o dispositivo marca al dispositivo correspondiente como en línea, y un DEATH lo marca como fuera de línea, de forma inmediata y explícita. Esto hace de Sparkplug un transporte que [afirma presencia](./device-presence.md), al igual que [LwM2M](./lwm2m.md): el estado en línea de un dispositivo Sparkplug es autoritativo, no inferido a partir de un tiempo de espera (timeout). Consulta [Gestión de muertes](#death-handling) para ver qué muertes aplica DeviceChain.

- **Alimenta la misma canalización.** Las mediciones decodificadas y los cambios de presencia fluyen hacia la canalización normal de decodificar → resolver → persistir. Todo lo que viene después —historial, estado en vivo, paneles y el motor de detección— trata la telemetría de Sparkplug exactamente igual que cualquier otra. Un mensaje con más de 256 valores de métrica se almacena como varios eventos consecutivos de como máximo 256 cada uno, hasta 16.384 métricas en un mensaje; de un mensaje que lleve más no se almacena ninguna lectura. Cada valor conserva su propia marca de tiempo, y un mensaje reentregado produce los mismos eventos en lugar de duplicados.

### Gestión de muertes {#death-handling}

DeviceChain es deliberadamente estricto sobre qué muertes aplica:

- **La secuencia de nacimiento debe coincidir.** Cuando el BIRTH de un nodo declara un número de secuencia de nacimiento (como exige la especificación Sparkplug), un DEATH se acepta solo cuando lleva uno que coincide. Un will obsoleto o duplicado que quedó de una conexión anterior se ignora en lugar de derribar una sesión que un BIRTH más nuevo ya restableció.
- **Sin secuencia de nacimiento, no hay correlación.** Un nodo cuyo BIRTH no declaró ningún número de secuencia de nacimiento no puede correlacionarse en absoluto, así que su muerte se acepta tal cual.
- **Dispositivos no nacidos.** Un DEATH de dispositivo para un dispositivo que nunca nació no emite nada en absoluto, porque no hay presencia que terminar.
- **La muerte de un nodo se propaga en cascada.** La muerte de un *nodo* marca fuera de línea al nodo y a todos los dispositivos conocidos bajo él, porque en Sparkplug la muerte de un nodo implica que sus dispositivos se van con él.

## Multitenencia y configuración

Cada **fuente** de Sparkplug se configura para un inquilino. Contiene la URL del broker, las credenciales (suministradas como un secreto proyectado, nunca en configuración en texto plano) y los grupos a los que suscribirse. Las fuentes se entregan a `dcctl` como un archivo, con `--sparkplug-source`; [Cómo operar los servicios de borde](../deployment/edge-services.md#sparkplug-sources) lo muestra.

Todo mensaje que llega por una fuente se atribuye *al inquilino de esa fuente*. El inquilino queda fijado por el broker por el que llegó el mensaje y nunca se lee del tópico de Sparkplug, así que la red de borde de un inquilino nunca puede confundirse con la de otro.

### Un nodo que nace antes de que exista su tipo de dispositivo {#birth-recovery}

Un nodo se registra la primera vez que llega su nacimiento, con el `deviceTypeToken` que nombra su fuente. Si ese tipo de dispositivo aún no existe (el error de orden habitual cuando se conecta una fuente antes de aprovisionar el inquilino), o device-management no responde durante más tiempo que el breve reintento, el nacimiento se descarta y se cuenta. Un nodo que ya nació no vuelve a anunciarse por sí solo, así que seguiría siendo desconocido.

Por eso la plataforma recuerda qué nodos no pudo registrar y **pide a cada uno que vuelva a nacer** (un `NCMD` `Node Control/Rebirth`), 10 segundos después del fallo y luego a intervalos crecientes hasta una vez por minuto, hasta que un mensaje del nodo se registra. Crea el tipo de dispositivo y el nodo aparece en menos de un minuto, sin reiniciar nada. No se reproduce nada desde memoria: los nacimientos que responden se ingieren como cualquier otro, de modo que un nodo nunca puede registrarse dos veces. El primer fallo de un nodo se registra en el log como error con su causa, cada petición como aviso, y `birth_recovery_pending` se mantiene por encima de cero mientras algún nodo espera (consulta [las métricas](../deployment/edge-services.md#sparkplug-ingestion-metrics)). Un nodo que se desconecta se olvida; su siguiente nacimiento es un intento nuevo.

## Alta disponibilidad

Solo **una** réplica del servicio de Sparkplug se conecta a un broker dado a la vez. Un Host de Sparkplug es propietario del estado de alias, secuencia y sesión por nodo, así que dos instancias conectadas verían cada una una parte del tráfico y publicarían `STATE` contradictorios. Por eso la propiedad única es un requisito de correctitud, no una opción de ajuste. La garantiza un arrendamiento (lease) de propiedad con vallado, y el despliegue se niega a renderizar más de una réplica.

El arrendamiento hace que el reemplazo sea seguro y automático. Cuando el pod que sirve desaparece —una caída, un desalojo, la pérdida de un nodo o un despliegue nuevo—, su reemplazo no puede empezar a servir hasta haber adquirido el arrendamiento, de modo que nunca hay una ventana con dos Hosts en el broker. Al adquirir el arrendamiento, el nuevo líder:

1. Restablece la sesión, pidiendo a los nodos que se vuelvan a anunciar.
2. Reconcilia la presencia de los dispositivos, de modo que una desconexión ocurrida durante el relevo no se pierde y ningún dispositivo queda mostrado incorrectamente como en línea.

La recuperación es automática pero no instantánea. Tarda lo que el pod de reemplazo necesite para planificarse y arrancar, más hasta 30 segundos de la ventana de vallado del arrendamiento. No hay un standby en caliente manteniendo una segunda conexión, porque eso sería precisamente lo que la propiedad única existe para evitar.

:::note Estado
La ingesta de Sparkplug-B está disponible como un servicio opcional (opt-in). Ingiere mediciones e impulsa la [presencia de dispositivo](./device-presence.md) autoritativa. Se conecta a un broker por TLS o texto plano, según la URL configurada. Un segundo protocolo de borde nativo de estándares, [LwM2M](./lwm2m.md), también está disponible. El CA personalizado / mTLS hacia un broker privado está planificado.
:::

## Comandos {#commands}

DeviceChain puede enviar comandos **hacia abajo** a un nodo o dispositivo Sparkplug, por la misma sesión de Host Application que ya mantiene con tu bróker. Es **opcional y está desactivado por defecto**: hasta que una fuente lo habilita, un comando a un dispositivo Sparkplug falla al instante como no entregable, igual que antes. Habilitarlo es un ajuste de la fuente, descrito en [Comandos Sparkplug](../deployment/edge-services.md#sparkplug-commands).

Son [comandos](./commands.md) normales: se declaran en el perfil del dispositivo, se envían desde la página del dispositivo, un lote o una regla de automatización, y están sujetos a los mismos permisos, ventanas de mantenimiento y registro de auditoría. El botón **Añadir comandos estándar de Sparkplug** de la consola declara en un perfil los tres siguientes.

| Clave de comando | Qué envía | Se permite cuando |
| --- | --- | --- |
| `sparkplug.write` con `{"metric": nombre, "value": v}` | Un `DCMD` a un dispositivo, o un `NCMD` a un nodo, que fija una métrica | La métrica está en la lista de escritura de la fuente y el nodo la declaró en su nacimiento |
| `sparkplug.rebirth` | Un `NCMD` que pide al nodo publicar de nuevo su nacimiento, con todos los valores actuales | Siempre, una vez habilitados los comandos |
| `sparkplug.reboot` | Un `NCMD` que fija `Node Control/Reboot` | El comando está declarado `DISRUPTIVE` o `DESTRUCTIVE`, la fuente tiene `allowReboot`, el nodo declaró la métrica en su nacimiento, está en línea y no se ha reiniciado dentro de la ventana de reinicio |

Si el comando va a un nodo o a un dispositivo depende del destino: un dispositivo bajo un nodo recibe un `DCMD`, un nodo recibe un `NCMD`. Una métrica a la que el nacimiento dio un alias se direcciona por alias, como exige Sparkplug; si no, por nombre. El valor se codifica en el tipo de dato que declaró el nodo, y un valor de tipo incorrecto se rechaza antes de enviar nada, con la métrica, el tipo de dato y tu valor en el motivo. No se admiten arrays, data sets ni otros tipos estructurados. Un comando se publica una sola vez, con QoS 0 y sin retener.

Si el dispositivo aún no ha nacido en la sesión (el nodo está fuera de línea, o la plataforma acaba de reiniciarse y los nacimientos no han llegado), el comando queda **aparcado** y se envía cuando llega el nacimiento. El reinicio es la excepción: solo se envía a un nodo en línea y, si no, se rechaza.

### Qué significan los resultados {#command-outcomes}

**Sparkplug no tiene confirmación de comandos.** Un nodo que recibe un `DCMD` no lo responde. Por eso la plataforma publica el comando y espera a que el nodo **informe** del valor solicitado en un mensaje posterior de datos o de nacimiento, y el resultado indica cuál de estos casos ocurrió:

| Estado | Significado |
| --- | --- |
| `SENT` | Publicado; esperando a que el nodo informe del valor. Dura hasta la ventana de confirmación |
| `SUCCESSFUL`, informado | El nodo informó de la métrica con el valor solicitado después de publicar el comando. El resultado registra la métrica, el valor, cuándo se informó y en qué mensaje |
| `SUCCESSFUL`, ya en el valor | El nodo ya había informado de ese valor y no informó de nada distinto dentro de la ventana. La plataforma no vio que el nodo actuara por este comando |
| `TIMEOUT` | Nada lo confirmó dentro de la ventana (2 minutos por defecto). **No es un fallo**: el nodo pudo ignorar o rechazar el comando, el bróker pudo descartarlo, o el nodo obedeció y aún no ha informado |
| `FAILED`, no entregado | Rechazado antes de enviar nada: comandos no habilitados para esa fuente, un valor no válido, una métrica no permitida, un comando caducado o el cortacircuitos de abajo. La respuesta almacenada lleva un [código de motivo](#command-refusals) que dice cuál |

De ahí se desprenden dos precauciones. *Confirmado* significa que la métrica **alcanzó** el valor, no que esta solicitud lo causara: Sparkplug no autentica quién publica, así que cualquiera que pueda publicar en el topic del nodo en tu bróker puede hacer que una métrica lea cualquier valor, y las ACL de publicación del bróker son lo que respalda la evidencia. Y una métrica que un nodo informa solo al cambiar puede dejar un comando en `TIMEOUT` aunque funcionara. Un informe anterior al comando y uno marcado como histórico nunca cuentan como confirmación.

Una escritura más reciente a la misma métrica del mismo dispositivo cierra la anterior que sigue esperando, como `TIMEOUT` con el motivo *superseded*. Un rebirth se confirma con el siguiente nacimiento del nodo, y un reinicio con un nacimiento con un nuevo número de secuencia de nacimiento, dentro de su propia ventana, más larga.

### Por qué se rechazó un comando {#command-refusals}

Un comando rechazado antes de enviar nada acaba como `FAILED`, no entregado, y su respuesta almacenada es `{"reason": "<código>"}`. El texto del error dice lo mismo con palabras; el código es lo que debe usar un script o la consola, porque la redacción puede cambiar y los códigos son un conjunto fijo. La consola muestra una frase para cada uno.

| Código | Significado |
| --- | --- |
| `commands_not_enabled` | Los comandos están desactivados para el inquilino o para la fuente en la que está el dispositivo |
| `broker_refused` | El cortacircuitos está abierto: el broker rechazó una publicación anterior |
| `expired` | La caducidad del propio comando pasó antes de poder enviarlo |
| `invalid_parameters` | Los parámetros no son los que admite el comando (una escritura necesita el nombre de una métrica y un valor escalar; un rebirth y un reboot no admiten ninguno) |
| `unknown_command` | No es `sparkplug.write`, `sparkplug.rebirth` ni `sparkplug.reboot` |
| `metric_protocol_control` | La métrica es una métrica de control del protocolo (`bdSeq`, `Node Control/...`, `Device Control/...`) |
| `metric_not_writable` | La métrica no está en la lista `writableMetrics` de la fuente |
| `metric_not_declared` | El nacimiento del nodo no declaró la métrica |
| `datatype_unsupported` | El tipo de dato de la métrica es uno que los comandos no transportan |
| `value_not_accepted` | El valor no se puede escribir como el tipo de dato que declaró el nodo, o está fuera de su rango |
| `datatype_changed` | El nodo volvió a nacer con otro tipo de dato para la métrica mientras el comando esperaba |
| `node_rebirthing` | El nodo volvió a nacer repetidamente mientras se registraba el comando |
| `node_offline` | Un reinicio solo se envía a un nodo que está en línea en el momento del envío |
| `reboot_impact` | El comando de reinicio no está declarado disruptivo ni destructivo en el perfil |
| `reboot_not_allowed` | `allowReboot` está desactivado en la fuente |
| `reboot_not_a_node` | Un reinicio se dirigió a un dispositivo y no a un nodo de borde |
| `reboot_not_declared` | El nodo no declaró `Node Control/Reboot` en su nacimiento |
| `reboot_not_boolean` | El nodo declara `Node Control/Reboot` con un tipo de dato distinto de Boolean |
| `reboot_unverified` | Aún no se pudo leer el registro de reinicios recientes, así que no se pudo aplicar la regla de un reinicio por ventana |
| `reboot_rate_limited` | El nodo ya se reinició dentro de la ventana de reinicio |
| `no_dispatch` | No se pudo dar seguimiento a la entrega, así que el comando no se aparcó ni se envió |
| `publish_failed` | Falló la publicación en tu broker |

### Host primario y secundario {#primary-and-secondary-host}

Sparkplug recomienda que solo el **host primario** de un nodo le envíe comandos. Una fuente declara cuál es con `hostRole`, que no tiene valor por defecto porque es un hecho sobre tu planta:

- **`primary`**: tus nodos nombran el host id de esta fuente como su host primario. Está disponible el resultado *ya en el valor*.
- **`secondary`**: otro host es el primario. Los comandos se siguen enviando, pero un nodo, o las ACL del bróker, pueden ignorarlos o bloquearlos, y el primario puede volver a escribir el valor. El resultado *ya en el valor* se desactiva, porque el otro host puede ser quien mantiene el valor, y la consola marca con una advertencia un comando confirmado por un host secundario.

### Cuando el bróker rechaza la publicación {#command-breaker}

Un bróker que rechaza una publicación puede cerrar toda la sesión, lo que marca la fuente como fuera de línea y hace que todos los nodos hagan rebirth, y lo repetiría con cada comando en cola. Por eso cada fuente tiene un **cortacircuitos**: si la sesión se pierde en los 5 segundos siguientes a publicar un comando, el cortacircuitos se abre, ese comando acaba sin confirmar con un motivo que indica que el bróker probablemente lo rechazó, y los comandos a esa fuente fallan al instante como no entregados durante 5 minutos, duplicándose hasta 1 hora en cada reapertura. La telemetría no se ve afectada. Tras la pausa se deja pasar un comando para probarlo, y el cortacircuitos se cierra cuando se publica sin desconexión. La causa habitual es una credencial que no puede publicar en los topics de comandos; consulta [la ACL del bróker](../deployment/edge-services.md#sparkplug-commands-acl).

## Actualizar un nodo perimetral {#updating-an-edge-node}

Sparkplug B no define actualización de firmware, así que las [actualizaciones over-the-air](./over-the-air-updates.md) de DeviceChain no llegan a un dispositivo a través de este servicio, y DeviceChain no inventa una convención de firmware sobre Sparkplug. Un nodo perimetral que deba actualizarse a través de DeviceChain se conecta también a DeviceChain como un dispositivo MQTT ordinario, con su propia [credencial](../guides/device-credentials.md), y usa el canal de actualización normal. [Construir un dispositivo actualizable](../guides/building-an-updatable-device.md) muestra ese lado del dispositivo.

## Operación {#running-it}

Como una única réplica es propietaria de la conexión al broker, la ingesta de Sparkplug tiene algunas propiedades operativas que conviene conocer antes de depender de ella en producción: qué cuesta un relevo, por qué un dispositivo puede quedarse mostrando en línea y qué te están diciendo los contadores de descartes. [Cómo operar los servicios de borde](../deployment/edge-services.md) las cubre.
