---
title: Conectores
sidebar_position: 12
---

import GuideShot from '@site/src/components/GuideShot';

# Conectores

Un **conector** es un destino de salida al que una regla de detección puede publicar: un broker MQTT,
un tema de Apache Kafka, un tema de AWS SNS o una cola de AWS SQS. Cuando una regla se dispara, su
acción `publish` envía un mensaje a través del conector que indiques. Este capítulo muestra cómo crear
un conector, configurarlo y gestionar sus versiones. Es para los administradores de un inquilino que
conectan DeviceChain con los sistemas que ya utilizan.

Un webhook puntual no necesita conector: la acción de webhook de una regla lleva su propia URL. Para
saber cómo funcionan y se gobiernan las acciones de salida, consulta
[conectores de salida](../concepts/outbound-connectors.md).

El servicio de conectores no forma parte de todas las instalaciones. Si tu instancia no lo ejecuta,
las páginas de **Conectores** lo indican en lugar de mostrar una lista.

## La lista de conectores

Abre **Conectar → Conectores** en la barra lateral. La lista muestra el nombre, el tipo y la descripción de cada
conector. Haz clic en una fila para abrirlo. **Eliminar**, en una fila, borra el conector tras
confirmarlo. Cualquier regla que publique a través de un conector eliminado no podrá entregar hasta
que la apuntes a otro, y la eliminación no se puede deshacer.

<GuideShot id="connectors-list" alt="La lista Conectores con un conector, Facilities event broker, de tipo MQTT, descrito como el que publica eventos de alarma en el broker del equipo de instalaciones. La fila tiene la acción Eliminar, y arriba a la derecha está el botón Nuevo conector." />

## Crear un conector

Haz clic en **Nuevo conector**. El panel empieza con los campos que tienen todos los conectores:

- **Token**: el identificador con el que las reglas nombran el conector. El botón de al lado genera
  uno a partir del patrón de tokens de tu instancia.
- **Nombre** y **Descripción**: texto libre.
- **Tipo**: el transporte a través del cual publica el conector: MQTT, Apache Kafka, AWS SNS o AWS SQS.
  Cambiar el tipo vacía los campos de abajo, porque cada tipo necesita ajustes distintos.

El resto del formulario depende del tipo. Los campos marcados con `*` son obligatorios.

<GuideShot id="connectors-new" alt="El panel Nuevo conector con Token, Nombre, Descripción y Tipo en MQTT, seguidos de los campos de MQTT: URLs de los brokers, Tema, QoS en Predeterminado (1) e ID de cliente." />

| Tipo | Ajustes |
| --- | --- |
| MQTT | **URLs de los brokers** (una por línea, cada una con un puerto explícito y el esquema `tcp://`, `mqtt://`, `ssl://`, `tls://`, `mqtts://`, `ws://` o `wss://`), **Tema**, **QoS** (0, 1 o 2; 1 por defecto), **ID de cliente** y **Usuario** opcionales, y una **Contraseña** opcional. |
| Apache Kafka | **Direcciones de los brokers** (`host:puerto`, una por línea), **Tema**, un **ID de cliente** opcional, **Usar TLS** y autenticación SASL opcional, con un mecanismo, un usuario y una contraseña. |
| AWS SNS | **Región**, **ARN del tema**, **ID de clave de acceso**, la **Clave de acceso secreta** obligatoria y una **Anulación del punto de conexión** opcional. |
| AWS SQS | **Región**, **URL de la cola**, **ID de clave de acceso**, la **Clave de acceso secreta** obligatoria y una **Anulación del punto de conexión** opcional. |

La contraseña o la clave secreta es la **credencial** del conector. Es de solo escritura: una vez
guardada, la consola solo te dice que hay una credencial configurada y nunca vuelve a mostrarla. Para
cambiarla, haz clic en **Reemplazar** y escribe un valor nuevo; para quitarla, haz clic en **Limpiar**, y
**Deshacer** anula esa limpieza antes de guardar. **Limpiar** no se ofrece cuando el tipo exige una
credencial: siempre en AWS, y en Kafka cuando SASL está activado.

La consola comprueba los campos obligatorios sobre la marcha, pero el servidor vuelve a comprobar
toda la configuración al guardar y de nuevo cuando el conector envía. Cuando un conector envía, la plataforma rechaza
además un destino que resuelva a una dirección privada o interna, salvo que tu operador lo haya
permitido. Consulta [a dónde puede enviar un conector](../concepts/outbound-connectors.md#destinations).

Haz clic en **Crear conector** para guardarlo. Un conector nuevo es un **borrador**: ninguna regla puede
publicar a través de él hasta que publiques una versión, como se explica más abajo.

## Editar un conector

Abre un conector para ver su página de detalle. La pestaña **Configuración** contiene el mismo
formulario que el panel de creación. El tipo se muestra, pero no se puede cambiar; para pasar a otro
transporte, crea un conector nuevo.

<GuideShot id="connector-config" alt="La pestaña Configuración del conector Facilities event broker: Nombre, Descripción, el Tipo MQTT de solo lectura, URLs de los brokers con una dirección mqtts en el puerto 8883, el Tema acme/building-ops/alarms y QoS en 2, exactamente una vez." />

Edita los campos y haz clic en **Guardar borrador**. Mientras tengas cambios sin guardar, la consola
muestra **Cambios sin guardar** junto al botón. Guardar solo cambia el borrador. Las reglas siguen
publicando a través de la última versión publicada hasta que vuelvas a publicar.

## Versiones

La pestaña **Versiones** lista todas las versiones publicadas, de la más reciente a la más antigua,
con su etiqueta, cuándo se publicó y quién la publicó. La más reciente está marcada como **en vivo**, y
la línea sobre la tabla indica la versión a través de la cual despachan las reglas.

<GuideShot id="connector-versions" alt="La pestaña Versiones del conector Facilities event broker. Una línea indica que las reglas se despachan a través de la versión 2, la más reciente publicada. Hay dos versiones: la 2, marcada en vivo, con la etiqueta Route to building-ops topic, y la 1, con la etiqueta Initial, cada una con su fecha de publicación, quién la publicó y la acción Restaurar. Arriba a la derecha está el botón Publicar." />

- **Publicar** congela el borrador guardado en una versión nueva e inmutable, que pasa a ser la que
  usan las reglas. Puedes añadir una etiqueta y una descripción. Guarda antes el borrador: **Publicar**
  está desactivado mientras haya cambios sin guardar.
- **Restaurar** copia la configuración de una versión anterior en el borrador y reemplaza los cambios
  sin guardar. No cambia lo que usan las reglas: publica después para convertirla en la versión en
  vivo. La credencial no tiene versiones, así que restaurar una versión deja la credencial actual
  como está.

## Relacionado

- [Conectores de salida](../concepts/outbound-connectors.md): las acciones de publicación y de
  webhook, la entrega, los destinos y la gobernanza.
- [Procesamiento de eventos](../concepts/event-processing.md): las reglas que publican a través de
  los conectores.
