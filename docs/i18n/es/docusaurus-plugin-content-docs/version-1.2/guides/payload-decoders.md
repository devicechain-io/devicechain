---
sidebar_position: 3.8
title: Decodificadores de payload
---

# Decodificadores de payload {#payload-decoders}

Un **decodificador de payload** convierte lo que envió un dispositivo o un servidor de red en mediciones. Lo escribes una vez, en un [perfil de dispositivo](../user-guide/device-types-and-profiles.md), como una lista corta de reglas: lee dos bytes en el desplazamiento 1 como un número con signo, divídelo entre diez y guárdalo como `temperature`. Lee una trama binaria, un cuerpo JSON o el objeto que un servidor de red ya decodificó, y produce las mismas mediciones que cualquier otro origen, con las unidades y la validación de las métricas que declaraste.

:::note La decodificación en vivo está en la v1.2
Un decodificador que publicas se ejecuta sobre los uplinks en vivo de una [integración de entrada](./inbound-integrations.md): cuando el dispositivo del que trata un uplink tiene un tipo de dispositivo cuya versión de perfil lleva un decodificador, el uplink se decodifica con él en lugar de guardarse como lo decodificó el servidor de red. Un decodificador recién publicado puede tardar hasta un minuto en aplicarse. Un tipo de dispositivo sin decodificador se guarda como antes. Cómo desplegar la actualización se describe en [Versiones y actualizaciones](../deployment/releases-and-upgrades.md#v120-payload-decoders).
:::

## Cuándo necesitas uno {#when}

Probablemente no necesitas uno cuando el servidor de red ya decodifica la trama. ChirpStack con un códec y The Things Stack con un payload formatter envían un objeto decodificado, y una integración [guarda sus números tal cual](./inbound-integrations.md#what-is-stored). Necesitas un decodificador cuando:

- el servidor de red no tiene códec ni formatter para el dispositivo y conoces la disposición de bytes de sus tramas;
- quieres renombrar, reescalar o remodelar lo que decodificó el servidor de red, por ejemplo convertir Fahrenheit a Celsius o leer `bat.mv` como `battery`;
- el remitente es una integración de [JSON genérico](./inbound-integrations.md#generic-json) o de [binario genérico](./inbound-integrations.md#generic-binary) y quieres más que los números que aplana;
- la trama lleva un payload Cayenne LPP, que `lpp.decode` lee en una sola llamada.

## Qué es un decodificador {#what-it-is}

Un decodificador pertenece a un perfil de dispositivo y viaja con él:

- Lo editas en el **borrador** del perfil, en la pestaña **Decodificador**. Guardar escribe solo el borrador.
- **Publicar** el perfil congela el decodificador en esa versión. El decodificador de una versión publicada no cambia nunca, y volver a una versión anterior recupera su decodificador con ella.
- Un uplink se decodifica con el decodificador de la versión por la que se resuelve su dispositivo.

El decodificador es un documento de expresiones en un lenguaje pequeño y sin bucles (CEL). Tiene un tipo de **entrada** que lee, **valores compartidos** opcionales y una o más **secciones**; cada sección tiene **filas**, y cada fila indica qué métrica escribe y cómo calcular el valor. El lenguaje no tiene reloj, ni aleatoriedad, ni efectos secundarios, así que la misma entrada da siempre las mismas lecturas.

Un decodificador puede tener como máximo 8 secciones, 16 valores compartidos y 20 vectores guardados, y el documento completo ocupa como máximo 32 KiB. La [referencia de funciones](../reference/decoder-functions.md#decoder-functions-limits) enumera los límites.

## Entradas {#inputs}

La **entrada** del decodificador indica qué parte de un uplink lee. Un uplink que no lleva esa parte falla al decodificarse con `missing_input`; nunca se lee como cero.

| Entrada | Qué es |
| --- | --- |
| **Trama binaria** (`FRAME`) | Los bytes crudos del mensaje del dispositivo, como máximo 4 KiB. |
| **JSON genérico** (`JSON`) | Un elemento de una integración de JSON genérico, sin los campos que nombran el dispositivo y la hora. Como máximo 64 KiB, anidado como máximo 32 niveles. |
| **Objeto decodificado** (`DECODED`) | El objeto que decodificó el servidor de red (el `object` de ChirpStack, el `decoded_payload` de The Things Stack), con los mismos límites. |

Una expresión ve estos nombres:

| Nombre | Tipo | Significado |
| --- | --- | --- |
| `frame` | bytes | La trama cruda. |
| `json` | mapa | El elemento JSON genérico. |
| `decoded` | mapa | El objeto decodificado del servidor de red. |
| `fPort` | int | El puerto de aplicación LoRaWAN, o `-1` cuando el uplink no vino de un servidor de red LoRaWAN. |
| `fCnt` | int | El contador de tramas ascendente de LoRaWAN, o `-1`. |
| `link` | mapa de doubles | `rssi`, `snr` y `gateways`, cuando el servidor de red los informó y la integración tiene activado **Guardar métricas del enlace**. |
| tus valores compartidos | el tipo de su expresión | Cualquier valor al que hayas puesto nombre en **Valores compartidos**, utilizable por todo lo que viene después. |

Los dos mapas de entrada se leen con claves constantes: `decoded.battery.mv` o `decoded["battery"]["mv"]`. Una clave no se puede calcular, así que un arreglo JSON no se puede indexar. Para leer una clave que puede faltar, compruébala antes con `has(decoded.humidity)`.

ChirpStack aporta `frame` (`data`), `decoded` (`object`), `fPort` y `fCnt`. The Things Stack aporta `frame` (`frm_payload`), `decoded` (`decoded_payload`), `fPort` (`f_port`) y `fCnt` (`f_cnt`). Una integración de JSON genérico aporta `json`, y también `frame` cuando configuras su `framePointer`. Una integración binaria genérica aporta `frame`.

## Secciones y campos {#sections}

Un decodificador es una lista de **secciones**, y **se ejecuta la primera sección cuya condición se cumple**. Una sección sin condición coincide siempre. Usa las secciones para dar a cada tipo de mensaje su propia disposición, normalmente por `fPort`: la pestaña Decodificador tiene un cuadro **fPort** que es un atajo para la condición `fPort == 2`, y un cuadro **Solo cuando** para cualquier otra condición.

- **Una sección marcada para ignorar** acepta el uplink y no guarda nada. Úsala para los tipos de mensaje que no quieres. Se cuenta como ignorada, no como fallida.
- **Que ninguna sección coincida** hace fallar la decodificación con `no_section`. Pon al final una sección que ignore, sin condición, si quieres que todos los demás mensajes se acepten en silencio.
- **Una sección que coincidió pero no produjo ninguna lectura** (la condición de cada fila fue falsa) falla con `no_outputs`.

Cada fila escribe una medición. Una fila es de uno de tres tipos.

### La tabla de campos {#field-table}

La pestaña Decodificador empieza con una **tabla de campos**, y cada fila suya se convierte en una expresión. Eliges:

- **Métrica**: la clave bajo la que se guarda el valor. Solo se ofrecen las métricas declaradas en el perfil como medidas, no calculadas.
- Una clave de métrica puede contener puntos (`battery.voltage`), pero el token de la métrica no: los tokens solo admiten letras, dígitos, `-` y `_`. Declara la métrica con un token como `battery-voltage` y conserva `battery.voltage` como su clave; la fila del decodificador nombra la clave.
- Para una trama: el **tipo** (entero sin signo o con signo de 8, 16, 24 o 32 bits, flotante de 32 o 64 bits, un solo bit, un campo de bits, un campo de bits con signo o dígitos BCD), el **orden de bytes** cuando importa y el **desplazamiento en bytes**. Un campo de bits también lleva un desplazamiento en bits y un ancho de 1 a 32; BCD lleva una longitud de 1 a 8 bytes.
- Para JSON: la **ruta** en el mensaje, como `bat.mv`, si es un número o un valor verdadero/falso, y si se omite la fila en silencio cuando la ruta no existe.
- Opcionalmente **multiplicar por**, **dividir entre** y **sumar**, aplicados en ese orden.
- Opcionalmente una condición **Solo cuando**. Una fila cuya condición es falsa se omite, sin error y sin recuento.

La tabla es una comodidad que escribe la expresión por ti. Una fila que conviertes en expresión sigue siendo una expresión: nada vuelve a convertir una expresión en una fila de la tabla.

### Expresiones libres {#raw-cel}

Una fila de tipo **Expresión** tiene una clave de métrica y una expresión libre. La expresión debe producir un número o un valor verdadero/falso; un valor verdadero/falso se guarda como `1` o `0`. Todo lo que la expresión puede llamar está en la [referencia de funciones](../reference/decoder-functions.md).

```text
double(i16be(frame, 1)) / 10.0
```

El lenguaje de expresiones es tipado, y tres reglas resuelven la mayoría de los primeros intentos:

- **Los números enteros se dividen como enteros.** `u16be(frame, 3) / 7` descarta el resto. Escribe `10.0`, o convierte con `double(...)`, cuando quieras una fracción.
- **Una comparación necesita números que el compilador sepa que son números.** `decoded.t > 1` se rechaza porque `decoded.t` podría ser cualquier cosa. Escribe `double(decoded.t) > 1.0`.
- **Un error que el otro lado de `&&` o `||` resuelve no es un error.** `has(decoded.x) && double(decoded.x) > 1.0` es simplemente falso cuando falta `x`. Cualquier otro error hace fallar la decodificación.

Los bits se cuentan desde el bit menos significativo del byte: `bit(frame, 0, 0)` es el bit más bajo del primer byte y `bit(frame, 0, 7)` es el más alto. Un campo de bits continúa en el byte siguiente, así que `bits(frame, 0, 4, 8)` sobre los bytes `AB CD` lee ocho bits a partir del bit 4 del primer byte y da `0xDA`.

### Mapas {#maps}

Una fila de tipo **Mapa** toma un grupo entero de claves de una sola expresión, como `lpp.decode(frame)`, o de un subobjeto de la entrada como `decoded.values`. Le das un **prefijo de las claves** y un **máximo de claves** (`maxKeys`, de 1 a 64):

- Cada clave del mapa se guarda como el prefijo más la clave. Una clave que no es una métrica declarada se omite, no es un error, porque un sensor puede añadir un canal; el panel de prueba la muestra como no almacenada.
- Un mapa con más de `maxKeys` claves hace fallar la decodificación con `map_overflow`. Nunca se recorta.
- Si más de una fila escribe la misma clave en un uplink, se conserva una entrada y las demás se omiten, y el panel de prueba las muestra como `collision`. Una fila de campo gana a un mapa; entre dos mapas, gana la fila anterior.
- Una clave que empieza por `lorawan.` se omite: ese prefijo pertenece a las métricas del enlace que añade la plataforma.

### Valores compartidos {#shared-values}

Un **valor compartido** es una expresión con nombre que se calcula una vez por uplink, antes de las secciones, en el orden en que los enumeras. Úsalo para algo que varias filas necesitan, como un byte de cabecera o un fragmento del payload:

```text
hdr = u8(frame, 0)
```

Un nombre se compone de letras, dígitos y `_`, no empieza por un dígito y no puede ser uno de los nombres incorporados (`frame`, `fPort`, `math`, etc.).

### Cuántas lecturas {#max-readings}

El **máximo de lecturas** del decodificador es lo más que puede producir un uplink: para la sección más grande, sus filas de campo y de expresión más el `maxKeys` de sus filas de mapa. Es como máximo 64 por sección, y se muestra junto al decodificador en la pestaña Decodificador como "Hasta N lecturas por mensaje". Un uplink de un tipo de dispositivo con decodificador se cuenta contra la asignación de ingesta del inquilino al aceptarse: como mínimo este número de lecturas (más tres si la integración guarda métricas de enlace), más para un decodificador costoso de ejecutar, y nunca más de 256, sea cual sea lo que acabe produciendo.

### Qué se guarda {#output-values}

Cada fila escalar debe nombrar una **métrica declarada y medida del mismo perfil**, y el valor se escribe en el tipo de esa métrica:

| Tipo de métrica | Cómo debe ser el valor |
| --- | --- |
| Entero | Un número entero dentro del rango de 64 bits. `12.0` se acepta, `12.5` se rechaza. |
| Double | Cualquier número finito. |
| Booleano | Un valor verdadero/falso, o exactamente `0` o `1`. |

Después el valor pasa por la validación propia de la métrica (su mínimo, su máximo y sus valores permitidos), igual que un valor enviado por un dispositivo. Un valor que no la supera detiene toda la decodificación con `invalid_output`. No hay resultados parciales: **si falla cualquier fila, no se guarda nada de ese uplink.**

Una clave se puede escribir una sola vez en una sección. Las claves `lorawan.*` están reservadas para las métricas del enlace y no pueden ser salidas, y una métrica calculada tampoco puede serlo, porque la plataforma descartaría el valor en favor del calculado. Todo esto se rechaza al guardar.

## Vectores y publicación {#vectors}

Un **vector** es una entrada guardada con las lecturas que debe producir: un nombre, una trama (hexadecimal o base64), un objeto JSON o un objeto decodificado, opcionalmente un `fPort` y un `fCnt`, y los pares de clave y valor esperados. Un vector solo pasa cuando el decodificador produce **exactamente** esas claves, con valores numéricamente iguales: una clave que falta lo hace fallar, y una de más también. Un vector para una sección que ignora no espera ninguna lectura.

Puedes guardar un decodificador con vectores que fallan, así que el trabajo en curso nunca se pierde. **No puedes publicar un perfil mientras falle alguno de sus vectores.** Al publicar un perfil se compila el decodificador con las métricas que se publican y se ejecuta cada vector guardado, y se rechaza la publicación nombrando la primera expresión o el primer vector que falla. Volver a una versión anterior ejecuta la misma comprobación sobre esa versión, y la rechaza cuando su decodificador ya no compila o falla alguno de sus vectores. Un decodificador puede tener hasta 20 vectores.

Conserva un vector para cada disposición de mensaje que envía el dispositivo y otro para cada caso que alguna vez falló. Así sabes que cambiar un decodificador no cambió lo que da un mensaje antiguo.

## El panel de prueba {#test-panel}

El panel **Prueba** de la pestaña Decodificador ejecuta el decodificador **tal como está en el editor, guardado o no**, con el compilador y el evaluador de la propia plataforma, contra las métricas del borrador del perfil. No se guarda nada. Eliges qué probar con:

- **Trama**: pega hexadecimal o base64 (se ignoran los espacios en blanco, como máximo 4.096 bytes) y, opcionalmente, un `fPort` y un `fCnt`.
- **JSON**: un objeto JSON, entregado a `json` o a `decoded`, el que lea el decodificador.
- **Cuerpo de webhook**: elige una integración guardada y pega el **cuerpo completo de la petición**, con sus cabeceras, parámetros de consulta, tipo de mensaje y tipo de contenido allí donde la integración los lee. El panel lo analiza con el mismo código que analiza una petición en vivo, así que lo que ves es lo que la integración entregará al decodificador. Si el cuerpo contiene varios uplinks, se prueba el primero y el panel indica cuántos había. Si la integración tiene activado **Guardar métricas del enlace**, las lecturas del enlace aparecen también en el resultado.

El resultado muestra:

- **Decodificado**, **Ignorado** o **Falló**, y el nombre de la sección que coincidió;
- las **lecturas**, tal como se guardarían;
- las claves **no almacenadas**, cada una con su motivo: `undeclared` (no hay una métrica declarada con ese nombre), `collision` (otra fila ya escribe la clave) o `reserved`;
- si falla, el **motivo**, la **expresión** que falló (como `sections[0].outputs[1].expr`), el mensaje y el desplazamiento en bytes que rechazó un lector;
- el **coste** que gastó la decodificación;
- cada vector guardado, tanto si pasa como si falla. **Guardar como vector** convierte en un vector una prueba correcta de Trama o de JSON; requiere permiso para editar dispositivos, y una prueba de cuerpo de webhook no se puede guardar directamente.

Un documento que no compila se informa en el mismo lugar, como un error junto al editor, no como un fallo de la petición.

## Límites de coste {#cost}

Un decodificador no puede hacer bucles, pero aun así puede ser caro: leer un fragmento largo, decodificar un texto largo o una trama LPP larga cuesta más que leer un número. Por eso cada expresión tiene un **coste**, una cuenta del trabajo que hace: aproximadamente 1 por cada nombre, literal, operador y conversión, el coste que figura en cada función de la [referencia de funciones](../reference/decoder-functions.md#decoder-functions-library) para la llamada, y 1 por clave para una fila de mapa.

- **Antes de ejecutarse**, la plataforma suma el peor caso. Una sola expresión por encima de **10.000**, o un decodificador completo por encima de **50.000**, se rechaza al guardar o publicar, y el error nombra la expresión. Una lectura completa de 4 KiB cuesta unos 4.100, así que los decodificadores normales quedan muy por debajo.
- **Mientras se ejecuta**, una decodificación que gasta más de lo que estimó se detiene con `cost_exceeded`. Eso no puede ocurrirle a un decodificador bien formado; es la valla que respalda la estimación.
- **Leer JSON cuesta 1 por byte de la entrada.** La estimación que muestra la pestaña Decodificador incluye el análisis del mayor JSON permitido (65.536), y por eso un decodificador sobre `json` o `decoded` muestra un coste estimado de decenas de miles aunque sus expresiones sean pequeñas. Esa cifra no cuenta para el límite de 50.000; un uplink real se valora por su tamaño real.

Una función que lee un número fijo de bytes cuesta 1 sea cual sea la longitud de la trama. Cortar, decodificar texto y `lpp.decode` cuestan según la longitud, así que corta antes de decodificar y decodifica solo lo que necesitas.

## Cuándo se rechaza una decodificación {#refusals}

### Al guardar y al publicar {#refusals-save}

Guardar, publicar y volver a una versión anterior rechazan un decodificador que no es sólido, e indican dónde. El error de GraphQL lleva un código en `extensions.code`:

| Código | Significado |
| --- | --- |
| `INVALID_DECODER` | El documento no se analiza (una clave desconocida se rechaza, no se ignora), no compila, nombra una métrica que no está declarada, es calculada o está reservada, escribe una clave dos veces, supera un límite de coste o de tamaño o, al publicar o volver a una versión, tiene un vector guardado que falla. El mensaje nombra la expresión o el vector. |
| `UNSUPPORTED_DECODER_KIND` | El documento dice `"kind": "WASM"`. Solo se ejecuta `CEL`; el otro tipo se rechaza en lugar de omitirse. |
| `RATE_LIMITED` | Demasiadas peticiones de prueba; consulta [el límite de frecuencia de las pruebas](#rate-limit). |
| `INVALID_VALUE` | Una petición de prueba nombró más de una entrada, o un cuerpo de webhook sin integración. |
| `LIMIT_EXCEEDED` | Una entrada de prueba supera su límite. |

Un guardado informa de todos los problemas que encontró el compilador de una sola vez, cada uno ubicado por su ruta, para que los corrijas juntos:

```text
sections[0].outputs[1].key: "nope" is not a declared measured metric of this profile (a calculated metric cannot be a decoder output)
```

### En una prueba o en un uplink {#refusal-reasons}

Una decodificación que falla informa de uno de estos motivos. No se guarda nada de ese uplink.

| Motivo | Significado |
| --- | --- |
| `compile_failed` | El decodificador no compila. |
| `env_unsupported` | El decodificador nombra una versión de la biblioteca de funciones que esta compilación no conoce. |
| `missing_input` | El uplink no lleva la entrada que lee el decodificador. |
| `no_section` | No se cumplió la condición de ninguna sección. |
| `no_outputs` | La sección que coincidió no produjo ninguna lectura. |
| `eval_error` | Falló una expresión: una lectura más allá del final de la trama, una clave JSON ausente leída sin `has()`, un valor que no es un número finito, una conversión fallida. Un error de lectura nombra la función y el desplazamiento, como en `u16be: reads 2 byte(s) at offset 3 of a 2-byte frame (offset 3)`. |
| `invalid_output` | Un valor no se puede guardar para su métrica: es del tipo incorrecto, no es finito o está fuera de los límites de la métrica. |
| `map_overflow` | Una fila de mapa produjo más claves que su `maxKeys`. |
| `cost_exceeded` | La decodificación gastó más de lo que estimó. |
| `input_too_large` | La trama o el JSON superan su límite. |
| `input_malformed` | El JSON no se analiza o se anida a más de 32 niveles, o la entrada de prueba no es hexadecimal o base64 válido. |
| `unsupported_encoding` | Un fallo del panel de prueba: el cuerpo pegado usa una codificación que la integración no acepta, como la codificación Protobuf de ChirpStack. |
| `ignored_message` | Un fallo del panel de prueba: la integración ignora este tipo de mensaje, como un join, así que no hay nada que decodificar. |

Un uplink cuya decodificación falla no se guarda y se registra como un evento fallido con el motivo `DECODE_FAILED`; el comienzo de su texto de error es el motivo de esta tabla. Se registra en la primera entrega y no se reintenta.

## Creación mediante GraphQL {#graphql}

Todo lo que hace la consola está disponible en la API GraphQL de device-management. Guardar y borrar requieren permiso para editar dispositivos; probar requiere permiso para leerlos, y se rechaza a quien tenga el acceso limitado a una parte del inquilino.

```graphql
# schema: device-management
mutation SaveDecoder($token: String!, $document: String!) {
  setDeviceProfileDecoder(profileToken: $token, document: $document) {
    token
  }
}
```

El `document` es el decodificador como texto JSON. `clearDeviceProfileDecoder(profileToken)` retira el decodificador del borrador; las versiones publicadas conservan el suyo. El decodificador del borrador del perfil es `DeviceProfile.decoder`, y el congelado en una versión es `DeviceProfileVersion.decoder`; ambos informan de `maxReadings` y `estimatedCost`.

```graphql
# schema: device-management
query TestDecoder($input: PayloadDecoderTestInput!) {
  testPayloadDecoder(input: $input) {
    ok
    ignored
    section
    cost
    readings { key value }
    skipped { key reason }
    error { reason path message offset }
    vectors { name ok error }
    uplinks
  }
}
```

Nombra como máximo una entrada en `PayloadDecoderTestInput`: `frameHex`, `frameBase64`, `json` o `webhookBody` con su `integrationToken`. No nombres ninguna para ejecutar solo los vectores guardados. La tabla de funciones que muestra la consola es `payloadDecoderFunctions`.

## Ejemplos prácticos {#examples}

### ChirpStack: una trama binaria {#example-chirpstack}

Un sensor de sala envía seis bytes por el puerto 2, y el perfil de dispositivo de ChirpStack no tiene códec.

| Bytes | Significado |
| --- | --- |
| 0 a 1 | Temperatura en décimas de grado, con signo, big-endian |
| 2 | Humedad en pasos de medio por ciento |
| 3 a 4 | Batería en milivoltios, big-endian |
| 5 | El bit 0 es el contacto de la puerta |

Declara las métricas en el perfil de dispositivo: `temperature` (double), `humidity` (double), `battery` (entero) y `door` (booleano). Después fija la entrada del decodificador en **Trama binaria** y añade dos secciones: una con **fPort** 2 y los cuatro campos, y otra que **ignora** todos los demás puertos. Este es el documento que guarda la pestaña:

```json
{
  "kind": "CEL",
  "envVersion": 1,
  "input": "FRAME",
  "sections": [
    {
      "name": "uplink",
      "when": "fPort == 2",
      "outputs": [
        { "key": "temperature", "expr": "double(i16be(frame, 0)) / 10.0" },
        { "key": "humidity", "expr": "double(u8(frame, 2)) / 2.0" },
        { "key": "battery", "expr": "u16be(frame, 3)" },
        { "key": "door", "expr": "bit(frame, 5, 0)" }
      ]
    },
    { "name": "other ports", "ignore": true }
  ],
  "vectors": [
    {
      "name": "room sensor",
      "input": { "frameHex": "00D75A0CE401", "fPort": 2 },
      "expect": { "temperature": 21.5, "humidity": 45, "battery": 3300, "door": 1 }
    },
    { "name": "other port", "input": { "frameHex": "00", "fPort": 9 }, "expect": {} }
  ]
}
```

En el panel de prueba, elige **Trama**, pega `00D75A0CE401`, pon `fPort` en 2 y ejecuta: las lecturas son `temperature` 21.5, `humidity` 45, `battery` 3300 y `door` 1. Ejecuta la misma trama con `fPort` 9 y el resultado es **Ignorado**. Envía solo los tres primeros bytes, `00D75A`, y el resultado es `eval_error` en `sections[0].outputs[2].expr`: `u16be: reads 2 byte(s) at offset 3 of a 3-byte frame (offset 3)`.

Para ensayar la petición completa y no solo los bytes, elige **Cuerpo de webhook**, selecciona la integración de ChirpStack y pega el cuerpo que envía ChirpStack.

### The Things Stack: remodelar un objeto decodificado {#example-tts}

El payload formatter de The Things Stack ya decodifica la trama, pero a `{"temp_f": 70.7, "bat": {"mv": 3300}, "rh": 45}`. Si no haces nada, la plataforma guarda `temp_f`, `bat.mv` y `rh`, ninguna con las claves de tu perfil. Un decodificador sobre la entrada **Objeto decodificado** las renombra y convierte la unidad:

```json
{
  "kind": "CEL",
  "envVersion": 1,
  "input": "DECODED",
  "sections": [
    {
      "name": "uplink",
      "outputs": [
        { "key": "temperature", "expr": "(double(decoded.temp_f) - 32.0) / 1.8" },
        { "key": "battery", "expr": "int(decoded.bat.mv)" },
        { "key": "humidity", "when": "has(decoded.rh)", "expr": "double(decoded.rh)" }
      ]
    }
  ],
  "vectors": [
    {
      "name": "with humidity",
      "input": { "decoded": { "temp_f": 70.7, "bat": { "mv": 3300 }, "rh": 45 } },
      "expect": { "temperature": 21.5, "battery": 3300, "humidity": 45 }
    },
    {
      "name": "without humidity",
      "input": { "decoded": { "temp_f": 70.7, "bat": { "mv": 3300 } } },
      "expect": { "temperature": 21.5, "battery": 3300 }
    }
  ]
}
```

La fila `humidity` tiene una condición para que un uplink sin `rh` se decodifique igualmente; sin ella, la clave ausente haría fallar toda la decodificación con `eval_error`. El coste estimado de este decodificador supera 65.000, casi todo por la reserva para analizar un objeto completo de 64 KiB; consulta [Límites de coste](#cost). En el panel de prueba, elige **Cuerpo de webhook**, selecciona la integración de The Things Stack, pega el cuerpo completo del webhook y pon el tipo de mensaje en `uplink-message`.

### Cayenne LPP {#example-lpp}

Un dispositivo que envía Cayenne LPP no necesita tabla de campos: una sola fila **Mapa** decodifica la trama completa. La trama `01 67 00 F5 02 68 6C` contiene dos canales: una temperatura (tipo `67`, en décimas de grado) en el canal 1 y una humedad (tipo `68`, en pasos de medio por ciento) en el canal 2.

```json
{
  "kind": "CEL",
  "envVersion": 1,
  "input": "FRAME",
  "sections": [
    {
      "name": "lpp",
      "outputs": [{ "map": "lpp.decode(frame)", "prefix": "lpp.", "maxKeys": 8 }]
    }
  ],
  "vectors": [
    {
      "name": "two channels",
      "input": { "frameHex": "016700F502686C" },
      "expect": { "lpp.temperature_1": 24.5, "lpp.humidity_2": 54 }
    }
  ]
}
```

`lpp.decode` nombra cada valor por su tipo y su canal, así que las claves son `lpp.temperature_1` y `lpp.humidity_2` con el prefijo. Declara esas como métricas. Un canal que no hayas declarado aparece como no almacenado, y una trama con más de ocho valores falla con `map_overflow`. Cuando el payload LPP va después de una cabecera, córtalo primero: `lpp.decode(slice(frame, 4, size(frame) - 4))`.

## Lo que un decodificador no hace {#limits}

Un decodificador es deliberadamente pequeño, para que se ejecute en cada uplink a las tasas de ingesta y no pueda dañar la plataforma.

- **Sin bucles.** No hay `for`, ni comprensión de listas, ni recursión. La única macro es `has()`. Los datos de longitud variable los gestiona una función incorporada como `lpp.decode`, no un bucle que escribas.
- **Sin red, archivos, reloj ni aleatoriedad.** Un decodificador no puede llamar al exterior, leer nada que no sea su entrada ni dar una respuesta distinta para la misma entrada.
- **Sin procesamiento de texto.** No hay funciones de cadenas ni expresiones regulares. Se puede decodificar texto desde hexadecimal o base64 y compararlo con `==`, y una salida es siempre un número o un valor verdadero/falso, nunca texto.
- **Sin listas ni claves calculadas.** Un arreglo JSON no se puede indexar y una clave es siempre una cadena constante. Los literales de lista existen solo para `in`, con 16 valores como máximo.
- **Sin otro motor.** `WASM` es un valor reservado que se rechaza, no un motor que se ejecute.
- **No puede fijar la hora de una lectura.** La hora propia del mensaje sigue siendo la autoritativa, y eso es lo que mantiene en funcionamiento la [detección de duplicados](./inbound-integrations.md#duplicates).
- **No puede escribir una métrica calculada ni una clave `lorawan.`.**

El comportamiento de una función no cambia nunca una vez publicada. Una corrección llega con un nombre nuevo en una versión nueva de la biblioteca, y un decodificador conserva en `envVersion` la versión para la que se escribió.

## El límite de frecuencia de las pruebas {#rate-limit}

`testPayloadDecoder` compila el decodificador en cada llamada, así que está limitado a **10 peticiones por segundo por inquilino, con una ráfaga de 20, en cada réplica de `device-management`**. El límite es por réplica, no por instancia: con varias réplicas el total del inquilino puede ser mayor. Una petición por encima del límite se rechaza con `RATE_LIMITED`; espera un momento y reintenta. La consola muestra "Demasiadas pruebas demasiado rápido". Guardar, publicar y la lista de funciones no tienen este límite.

## Relacionado {#related}

- [De un uplink LoRaWAN a una alarma](./lorawan-uplink-to-alarm.md): un decodificador, una integración, una regla y una alarma, de principio a fin, con el simulador como servidor de red.
- [Referencia de funciones del decodificador](../reference/decoder-functions.md): las funciones de la biblioteca con su coste, y los límites.
- [Integraciones de entrada](./inbound-integrations.md): los remitentes de los que lee un decodificador.
- [Tipos y perfiles de dispositivo](../user-guide/device-types-and-profiles.md): métricas, borradores y publicación.
- [Matriz de capacidades por transporte](../reference/transport-matrix.md#inbound-integrations): qué admite cada vía de entrada.
- [Versiones y actualizaciones](../deployment/releases-and-upgrades.md#v120-payload-decoders): qué cambió en esta versión.
