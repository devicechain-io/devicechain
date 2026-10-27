---
title: Ingesta LwM2M
---

# Ingesta LwM2M

Las flotas restringidas y celulares suelen hablar [**OMA LwM2M**](https://lwm2m.openmobilealliance.org/), un estándar compacto de gestión de dispositivos sobre CoAP. DeviceChain termina LwM2M directamente. Los dispositivos se conectan a él mediante **CoAP/UDP asegurado con DTLS**, y su registro, su telemetría y su firmware se asignan al mismo modelo de dispositivo que usa cualquier otro transporte.

La ingesta Sparkplug se conecta hacia *tu* broker. LwM2M funciona al revés: los dispositivos se conectan **hacia dentro**, al endpoint CoAP asegurado de DeviceChain, con la misma forma que la ruta MQTT estándar.

:::note Estado
La ingesta LwM2M está disponible como servicio opcional (opt-in) sobre CoAP/UDP con DTLS-PSK. Impulsa la [presencia de dispositivo](./device-presence.md) autoritativa, ingiere los objetos de sensor observados como mediciones y envía comandos Read/Write/Execute en sentido descendente (downlink), con retención y drenaje durables para dispositivos dormidos. Reiniciar y leer la información del dispositivo están disponibles [por nombre](#named-operations-and-device-information), y el fabricante, el modelo, el número de serie, la versión de firmware y el nivel de batería de cada dispositivo se leen cuando se registra y se guardan como atributos. La decodificación de Notify es **solo SenML-JSON**, así que un cliente solo LwM2M 1.0 obtiene presencia y comandos, pero ninguna medición hasta que llegue la decodificación TLV. El alcance de la disponibilidad general (GA) son las credenciales PSK; X.509 / clave pública sin procesar y un servidor Bootstrap están planeados.
:::

## Qué hace

### Autenticación en el handshake

Un dispositivo presenta una **identidad de clave precompartida (PSK) DTLS**. DeviceChain resuelve esa identidad a un inquilino y un dispositivo antes de que fluya cualquier tráfico de aplicación. Una identidad desconocida o malformada hace fallar el handshake y nunca llega al registro. La pertenencia a un inquilino (tenancy) proviene de la identidad *autenticada*, nunca de nada que el dispositivo declare en un payload.

Con el autorregistro habilitado para una credencial, la fila del dispositivo se crea la primera vez que se registra una identidad aprovisionada.

A los clientes itinerantes, cuya dirección de red cambia, se les sigue mediante el Connection ID de DTLS, de modo que un dispositivo celular conserva su sesión a través de un cambio de IP.

### Presencia a partir del ciclo de vida del registro

La presencia es autoritativa y sigue el ciclo de vida del registro de LwM2M:

- Un **Register** marca el dispositivo como en línea.
- Las **Updates** periódicas mantienen la sesión activa.
- Un **Deregister**, o un tiempo de vida de registro (registration lifetime) vencido, lo marca como fuera de línea.

Al igual que [Sparkplug-B](./sparkplug.md), esto convierte a LwM2M en un transporte [**que afirma presencia**](./device-presence.md) (presence-asserting): el estado en línea de un dispositivo es autoritativo, no se infiere a partir de un tiempo de espera.

### Los objetos de sensor se convierten en mediciones

DeviceChain **observa** (Observe) las instancias de objeto de *sensor* del dispositivo y decodifica cada **Notify** en mediciones tipadas dentro del sobre (envelope) normal. Así, la telemetría LwM2M llega al historial, al estado en vivo, a los paneles y al motor de detección exactamente igual que cualquier otra lectura.

- Los objetos de sensor son aquellos cuyo id de objeto cae dentro del rango fijo de sensores de IPSO Smart Objects (3200–3441) que el build trata como telemetría.
- La observación tiene un tope de 32 instancias por registro.
- Ni el rango ni el tope son un ajuste configurable.
- Los objetos de gestión (Security, Server, Device y el resto del conjunto OMA) nunca se observan.
- Un Notify con más de 256 valores numéricos se almacena como varios eventos de como máximo 256 cada uno, en lugar de recortarse. El techo de ingesta del inquilino, contado en lecturas, sigue aplicándose: un Notify mayor de lo que el techo puede admitir de una vez conserva los eventos que admite, y el resto se descarta y se cuenta en `ingest_samples_shed_total`.

Hoy solo se decodifican las notificaciones en **SenML-JSON**, y conviene que lo dimensiones de antemano: un cliente conforme solo LwM2M 1.0 no puede producirlas. SenML llegó con LwM2M 1.1, así que un cliente 1.0 responde correctamente al Observe con `4.06 Not Acceptable`. Ese dispositivo sigue registrándose, impulsa la presencia y acepta comandos, pero no reporta **ninguna telemetría**. Esta es hoy la mayor brecha funcional del soporte de LwM2M; decodificar el formato TLV más antiguo es el trabajo pendiente que la cierra. Ambos rechazos se contabilizan, de modo que un operador puede verlo ocurrir en lugar de deducirlo a partir de datos ausentes.

### Comandos y firmware

Los comandos de la plataforma se convierten en operaciones LwM2M de **Read / Write / Execute** sobre los recursos del dispositivo.

Una actualización de firmware se impulsa de la misma manera, como una secuencia que ejecuta el operador y no como una única operación de la plataforma. Eres tú quien emite los Write y el Execute contra el objeto estándar Firmware Update, como comandos ordinarios. DeviceChain mantiene los comandos de un mismo dispositivo en el orden en que los encolaste, así que un write de firmware y su execute nunca pueden reordenarse. No modela la actualización como un único trabajo gestionado, y las [actualizaciones por aire (OTA)](./over-the-air-updates.md) de la plataforma no llegan a los dispositivos LwM2M. Los pasos están en [Actualización de firmware con el objeto 5](#firmware-update-object-5).

Cuando un dispositivo tarda en responder y sus comandos se acumulan, los siguientes comandos para él se apartan y se entregan momentos después, todavía en orden. Un dispositivo lento no retrasa los comandos de los demás.

Un comando para un dispositivo que está dormido se **retiene de forma durable y se entrega en su próximo despertar** (modo cola), en lugar de descartarse. La retención tiene un horizonte acotado, así que un comando nunca espera indefinidamente. Mientras espera, el comando registra uno de dos estados, de modo que una acumulación real es una mezcla de ambos:

- [`PARKED`](./commands.md#parked-commands) en lugar de `SENT`.
- `HELD`, cuando la presencia ya ha marcado al dispositivo como ausente y el comando se retiene antes siquiera de publicarse.

En cualquier caso, la plataforma sigue teniendo el comando en su poder, así que todavía puedes cancelarlo. Si su tiempo de vida se agota antes, el registro dice que nunca llegó a un dispositivo en lugar de culpar al dispositivo por no responder.

### Actualización de firmware con el objeto 5 {#firmware-update-object-5}

Esta es la forma admitida de actualizar el firmware de un dispositivo LwM2M hoy. Usa tres comandos que el perfil del dispositivo declara como cualquier otro comando: `lwm2m.write`, `lwm2m.execute` y `lwm2m.read`, cada uno con un `path` en su payload (y, en una escritura, un `value` escalar). Emite los pasos **en orden y espera a que cada comando llegue a `SUCCESSFUL` antes de emitir el siguiente**. DeviceChain mantiene los comandos de un mismo dispositivo en el orden en que los encolaste, pero la propia máquina de estados de firmware del dispositivo también necesita el orden.

1. **Fija el URI del paquete.** Escribe la ubicación de la imagen en el recurso Package URI del objeto Firmware Update. El dispositivo empieza a descargar.

   ```json
   {"path": "/5/0/1", "value": "coaps://fw.example/image.bin"}
   ```

   Envía ese payload con `lwm2m.write`. La imagen se sirve desde donde tú la alojes; DeviceChain no la guarda ni entrega un enlace. Escribir el paquete en sí (`/5/0/0`) no está admitido, porque una escritura lleva un único valor escalar de como máximo 8 KiB.

2. **Dispara la actualización.** Cuando la descarga haya terminado, ejecuta el recurso Update.

   ```json
   {"path": "/5/0/2"}
   ```

   Envía ese payload con `lwm2m.execute`.

3. **Sigue el progreso y el resultado.** Lee el recurso State (`/5/0/3`; `0` es inactivo y `3` es actualizando) y el recurso Update Result (`/5/0/5`; `0` es inicial, `1` es éxito y `2` o más es un error) con `lwm2m.read`.

Leer es la única forma de seguir el progreso. DeviceChain solo observa el rango de objetos de sensores (3200 a 3441), así que el objeto 5 nunca se observa y su estado y resultado no llegan como mediciones. Para un dispositivo en modo cola, toda la secuencia sigue la misma vía de retención y entrega que cualquier comando: cada comando se retiene y se entrega cuando el dispositivo despierta, el más antiguo primero.

**Las actualizaciones gestionadas todavía no llegan a los dispositivos LwM2M.** Un artefacto, la retransmisión de una firma, un intento que termina en `UPDATED` y un despliegue necesitan cosas que este transporte no tiene hoy:

- Los enlaces de descarga de la plataforma son más largos de lo que admite el recurso Package URI: en la versión 1.0 del objeto 5 el recurso es una cadena de como máximo 255 bytes.
- El objeto 5 no tiene ningún recurso que lleve una firma. El propio formato de firmware del dispositivo hace su verificación, y la plataforma no tiene adónde retransmitir una firma, así que una asignación podría parecer autenticada sin serlo.
- LwM2M no tiene identificador de arranque, así que la prueba que necesita un intento, un arranque nuevo que ejecuta la versión objetivo, habría que inferirla de un registro nuevo y del objeto Device.
- La plataforma sirve las descargas solo por HTTPS, y un dispositivo puede admitir solo CoAP para descargar firmware.

Trata el procedimiento como la forma de actualizar dispositivos LwM2M. Las pruebas automáticas de interoperabilidad cubren el registro y la telemetría, no los comandos, así que prueba la secuencia con el modelo de tu dispositivo antes de depender de ella.

### Operaciones con nombre e información del dispositivo {#named-operations-and-device-information}

Dos operaciones estándar del objeto Device están disponibles por nombre, para que nadie tenga que escribir `/3/0/4`:

| Clave de comando | Qué hace |
| --- | --- |
| `lwm2m.reboot` | Ejecuta el recurso Reboot del objeto Device. No toma parámetros. |
| `lwm2m.readDeviceInfo` | Lee fabricante, número de modelo, número de serie, versión de firmware y nivel de batería, y responde con los valores leídos y una lista de los recursos que no pudo leer. |

Son comandos ordinarios. Decláralos en el perfil de dispositivo como cualquier otro comando (la pestaña Comandos del perfil tiene un botón **Añadir comandos estándar de LwM2M** que añade ambos) y pasan por la misma verificación al encolar, los mismos permisos, el mismo rastro de auditoría y la misma entrega con retención y drenaje que el resto de comandos. Un recurso que el dispositivo no tiene se informa como ausente, con el motivo, en lugar de hacer fallar toda la lectura.

`lwm2m.reboot` se crea con impacto `DISRUPTIVE`, así que un tipo de dispositivo con una [ventana de mantenimiento](./commands.md#maintenance-windows) lo retiene hasta que la ventana se abre. Un `lwm2m.execute` sin procesar no se controla.

El restablecimiento de fábrica (Factory Reset) no se ofrece por nombre a propósito: es destructivo y no se puede deshacer. Tampoco puede enviarse como un `lwm2m.execute` sin procesar: un Execute de `/3/0/5` (en cualquier instancia del objeto Device) se rechaza con `COMMAND_DESTRUCTIVE_REFUSED`, ya sea como comando individual, en un lote o desde una regla de automatización. Está prevista una operación con nombre y confirmación. La actualización de firmware también sigue siendo la secuencia del operador descrita en [Actualización de firmware con el objeto 5](#firmware-update-object-5).

La misma información del objeto Device también se recopila por ti. Cuando un dispositivo se registra, DeviceChain lee esos recursos una vez y los guarda en el dispositivo como **atributos de cliente** (`manufacturer`, `modelNumber`, `serialNumber`, `firmwareVersion`, `batteryLevel`), de modo que aparecen en la página del dispositivo. Los recursos que el dispositivo no informó se listan en el atributo `lwm2m.deviceInfoMissing`. Esto nunca retrasa el registro ni la telemetría: se ejecuta cuando el registro ya terminó, en el mismo orden por dispositivo que los comandos, pide cada recurso una sola vez, reintenta una sola vez a un dispositivo que no responde y lee un mismo dispositivo como máximo una vez por hora. Los valores que no han cambiado no se reescriben.

### El mismo pipeline

Las mediciones decodificadas y los cambios de presencia fluyen por la ruta normal de decodificación → resolución → persistencia, de modo que todo lo que sigue trata a los dispositivos LwM2M igual que a cualquier otro.

## Pertenencia a inquilino (tenancy) e identidad

Cada dispositivo se vincula a su inquilino mediante su **identidad PSK DTLS autenticada**, mapeada a un `(tenant, externalId)` en el momento de la conexión. Como la identidad se verifica durante el handshake, un dispositivo nunca puede presentar tráfico para otro inquilino. La identidad que viaja por la red es un identificador opaco en lugar de una cadena legible del tipo `tenant:device`.

## Alta disponibilidad

Una única réplica sirve el endpoint CoAP a la vez, sostenida por un arrendamiento (lease) de propiedad con vallado. Esto no es una opción de ajuste. Los dispositivos se conectan **hacia dentro** a un único socket UDP enlazado, así que una segunda réplica que compartiera el Service recibiría, y descartaría, silenciosamente una parte de los datagramas. Solo quien mantiene el arrendamiento enlaza el socket, y el despliegue se niega a renderizar más de una réplica.

El arrendamiento hace que **el reemplazo sea seguro y automático**. Un pod de reemplazo no enlaza nada hasta haber adquirido el arrendamiento, de modo que nunca hay dos procesos enlazados al endpoint a la vez. Como un dispositivo en modo cola puede permanecer en silencio durante largos períodos por diseño, el nuevo líder reconstruye la presencia a partir de la proyección durable en lugar de sondear. Un dispositivo que no vuelve a registrarse se marca como fuera de línea solo después de que haya pasado el tiempo de vida de registro máximo del servidor. Un relevo no marca falsamente como fuera de línea a dispositivos dormidos.

La recuperación es automática pero no instantánea. Tarda lo que el pod de reemplazo necesite para planificarse y enlazar, más hasta los 30 segundos de la ventana de vallado del arrendamiento. Los datagramas enviados durante esa ventana se pierden, y los mensajes CoAP confirmables se retransmiten, de modo que el relevo en sí pasa en gran medida desapercibido.

**Las observaciones no sobreviven a un relevo.** Las sesiones DTLS mueren con el proceso anterior y el nuevo líder arranca sin ninguna, así que un Observe no se vuelve a emitir hasta que el dispositivo se registra de nuevo por su propia cuenta. La presencia se reconstruye; la telemetría no.

El apagón está acotado únicamente por el tiempo de vida de registro de cada dispositivo. Para un dispositivo que usa el valor predeterminado de LwM2M, son 86400 segundos: un día entero de silencio de un dispositivo perfectamente sano.

Para acortar el apagón, acorta el tiempo de vida de registro que solicitan tus dispositivos, a costa de actualizaciones de registro más frecuentes. Elígelo según cuánto tiempo estás dispuesto a quedarte sin telemetría tras un reinicio.

El **tiempo de vida de registro máximo** del servidor (`maxLifetimeSeconds`) es el techo al que se recorta todo tiempo de vida de registro. Bajarlo no acorta el apagón, porque al dispositivo nunca se le informa del recorte y sigue actualizando según su propio calendario. Mantenlo por encima del tiempo de vida más largo que soliciten tus dispositivos. Un dispositivo que solicita un tiempo de vida más largo puede vencer entre sus propias actualizaciones, así que se marca como fuera de línea tanto en el funcionamiento normal como en cada relevo.

## Cómo se opera

El endpoint CoAP lo sirve una única réplica propietaria. Eso le da a LwM2M algunas propiedades operativas que conviene conocer antes de depender de él en producción: qué cuesta un relevo, por qué las observaciones no vuelven por sí solas y cómo acotar el hueco de telemetría. Todo ello se cubre en **[Cómo operar los servicios de borde](../deployment/edge-services.md)**.
