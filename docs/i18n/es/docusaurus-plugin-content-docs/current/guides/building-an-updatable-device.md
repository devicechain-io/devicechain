---
sidebar_position: 13
title: Construir un dispositivo actualizable
---

# Construir un dispositivo actualizable

Un dispositivo que recibe actualizaciones over-the-air tiene que hacer algo más que descargar un archivo. Esta guía recorre un pequeño dispositivo de referencia que hace lo que debe hacer un producto conectado real: seguir el protocolo de actualización, rechazar lo que debe rechazar, reemplazar su propio ejecutable y demostrar a la plataforma qué versión se está ejecutando de verdad.

El dispositivo de referencia está en el repositorio, en `examples/ota-device`. Es un programa en Go que se lee en una tarde, construido **solo a partir del protocolo publicado** en [Conexión de un dispositivo](./connecting-a-device.md#over-the-air-updates), de modo que una actualización correcta dice algo sobre ese protocolo y no sobre las partes internas de la plataforma. No importa nada de la plataforma, y una prueba falla si eso cambia.

:::note Un ejemplo, no un producto
Se compila y se prueba con el resto del repositorio, pero nunca se publica: no hay binarios publicados ni promesa de compatibilidad. Un renombrado de archivo que hace las veces de bootloader es un recurso didáctico, no algo que enviar a producción. Consulta [Lo que debe añadir un dispositivo real](#what-a-real-device-must-add).
:::

## Qué necesitas {#prerequisites}

- Una instancia que ejecute el servicio de actualizaciones y pueda dar a los dispositivos un enlace de descarga. Consulta [Antes de la primera actualización](../concepts/over-the-air-updates.md#first-update).
- Un dispositivo que se conecte por MQTT con su propia [credencial](./device-credentials.md). Solo los dispositivos MQTT pueden recibir una actualización gestionada; consulta [Qué dispositivos se pueden actualizar](../concepts/over-the-air-updates.md#transports).
- Un firmante y un artefacto firmado por él. Un artefacto que nadie ha firmado no se puede asignar salvo que la instancia permita artefactos sin verificar. Consulta [Firmar actualizaciones](./signing-updates.md).
- La clave pública del firmante en un archivo PEM del dispositivo. Es el **conjunto de confianza** del dispositivo: uno o más bloques `PUBLIC KEY`, concatenados.

## El lanzador y el firmware {#launcher-and-firmware}

El ejemplo son dos programas, porque instalar una imagen nueva significa que después del reinicio tiene que arrancar un programa distinto.

| Programa | Representa | Función |
| --- | --- | --- |
| `cmd/ota-boot` | El bootloader | Elige la ranura que se ejecuta. Una ranura recién instalada tiene exactamente un intento: si no se confirma a sí misma, el lanzador vuelve a ejecutar la última ranura confirmada y deja una nota que lo dice. Mata una ranura nueva que se cuelga sin confirmarse, y nunca se actualiza over-the-air. |
| `cmd/ota-device` | El firmware | Se conecta a la instancia por MQTT como un dispositivo, sigue el protocolo de actualización, instala una imagen verificada en la ranura inactiva y termina con el código de salida 75 («reiniciar») para que el lanzador lo arranque. |

Dos ranuras, `a` y `b`, guardan una imagen cada una. El directorio de estado registra cuál está **confirmada**, cuál está **pendiente** y cuántos intentos le quedan a la pendiente, en un archivo que se escribe de forma atómica. Actualizar siempre escribe la ranura que no se está ejecutando, de modo que la imagen en ejecución nunca se toca mientras corre.

La versión que el dispositivo informa como en ejecución es la compilada en el binario que se está ejecutando, y el `digest` que informa es el SHA-256 de ese mismo archivo. Nunca se lee de la configuración ni se copia de una asignación, así que no se puede convencer a un dispositivo de que diga que ejecuta algo que no ejecuta.

## El orden de las comprobaciones {#order-of-checks}

Cuando llega una asignación, el dispositivo hace esto en orden. El orden es lo importante: nada de lo descargado se ejecuta hasta que se han comprobado su integridad y su autenticidad.

1. **Política, antes de descargar nada.** El componente debe ser el de este dispositivo. Una asignación sin firmar se rechaza salvo que se haya indicado al dispositivo que acepte una. La versión debe ser versionado semántico válido y **estrictamente más reciente** que la que se ejecuta; una versión igual es una repetición, y una versión que no es semver se rechaza en lugar de adivinarse.
2. **Espacio en disco** para la parte de la descarga que falta.
3. **Descarga**, con peticiones `Range`, en un archivo parcial de la ranura inactiva. Una respuesta que empieza en un desplazamiento distinto del pedido se rechaza. Un byte más allá del tamaño declarado es un fallo y el archivo parcial se borra. Una concesión caducada pide una nueva, un número limitado de veces.
4. **Digest.** El SHA-256 de los bytes que tiene es igual a `artifact.digest`. No se ha ejecutado nada.
5. **Firma.** La firma separada sobre los 32 bytes sin procesar del digest se verifica contra una clave del conjunto de confianza propio del dispositivo. El `keyId` del mensaje solo elige una clave de ese conjunto; nunca se toma una clave de un mensaje. No se ha ejecutado nada.
6. **Identidad.** Solo ahora se ejecuta la imagen, una vez, en un modo que imprime su producto y su versión y termina, con un entorno vacío, un directorio de trabajo temporal y un plazo. Debe nombrar el producto de este dispositivo y la versión que nombró la asignación.
7. **Instalación.** La imagen pasa a la ranura inactiva, se deja pendiente, y el dispositivo informa `REBOOTING` y termina para que el lanzador lo arranque.

Cada etapa se escribe en disco antes de actuar sobre ella y antes de informarla. Por eso un enlace caído nunca cambia lo que hace el dispositivo: sigue instalando, y tras el reinicio informa de la etapa que ha alcanzado, omitiendo aquellas cuyo informe se perdió, algo que el protocolo permite. Un reinicio en cualquier punto continúa desde los bytes que tiene. El dispositivo solo instala mientras es la ranura confirmada y no hay nada pendiente.

## Reversión {#rollback}

Tras el reinicio, el proceso nuevo se confirma a sí mismo, lo que pasa la ranura de pendiente a confirmada, una vez que se ha informado a la plataforma de lo que se ejecuta. Si hay una actualización en curso, se confirma solo cuando lo que se ejecuta es la versión y el archivo de esa actualización. Después informa `RUNNING` con la versión compilada y el digest de su propio archivo.

Si la imagen nueva no lo consigue, el lanzador arranca la antigua. La imagen antigua informa `FAILED` con el código `ROLLED_BACK` y un detalle como `booted 1.0.1, expected 1.0.2`. Nunca informa `RUNNING` de una actualización que no se aplicó, de modo que el intento de la plataforma termina como fallo y no como un éxito que no podía demostrar.

## Rechazar degradaciones y componentes equivocados {#refusing-downgrades}

Una firma cubre solo el digest. No cubre la versión, el componente ni el producto, así que nada impide que una imagen más antigua correctamente firmada, o una imagen construida para otro producto, sea una imagen firmada perfectamente válida. Quien pueda publicar artefactos puede declarar de nuevo una imagen antigua bajo una versión nueva, y su firma antigua se sigue verificando. Consulta [Lo que una firma no cubre](./signing-updates.md#what-a-signature-does-not-cover).

Por eso el dispositivo es el único lugar que puede imponerlo, y el dispositivo de referencia lo hace dos veces. Antes de la descarga compara el componente y la versión de la asignación con los suyos. Después de la descarga ejecuta la imagen una vez y comprueba que nombra el producto de este dispositivo y la versión que nombró la asignación, de modo que una imagen que miente sobre sí misma se rechaza. No hay forma de desactivar la comprobación de degradación salvo una opción explícita de laboratorio, `--allow-downgrade`.

## Fijar el origen de la descarga {#grant-origin-pin}

El enlace de descarga lo elige la plataforma y se entrega por el broker. El dispositivo solo descarga del origen con el que se configuró, con el mismo esquema, host y puerto, sin información de usuario y con una ruta `/updates/download/`, y cualquier redirección debe permanecer en ese origen. Un componente mal configurado o comprometido que pueda publicar en el topic del dispositivo no puede hacer que el dispositivo descargue de otro host, ni que le envíe su capacidad.

El enlace lleva una capacidad en su cadena de consulta, que es un secreto al portador. El dispositivo nunca registra el enlace ni su consulta, y elimina los enlaces de los errores de transporte. No hay ningún interruptor que desactive la verificación de certificados. Un origen `http` sin cifrar se rechaza salvo que se indique `--allow-insecure-download`, y lo mismo ocurre con una URL de broker sin TLS salvo que se indique `--allow-insecure-broker`; ambas existen para un laboratorio en una instalación compacta sin TLS.

## Ejecutarlo {#run-it}

Desde `examples/ota-device`, compila los dos programas y coloca la primera imagen en la ranura `a`:

```bash
ROOT=$PWD/state
go build -ldflags "-X devicechain.io/examples/ota-device/internal/buildinfo.Version=1.0.0" -o bin/ota-device ./cmd/ota-device
go build -o bin/ota-boot ./cmd/ota-boot
mkdir -p "$ROOT/slots/a" && cp bin/ota-device "$ROOT/slots/a/"
```

En Windows, compila `ota-device.exe`; el lanzador espera ese nombre.

Pon la clave pública del firmante en un archivo de confianza y arranca el dispositivo bajo el lanzador:

```bash
openssl pkey -in signer.pem -pubout -out trust.pem

export OTA_CREDENTIAL_SECRET='<secreto de la credencial del dispositivo>'
bin/ota-boot --root "$ROOT" -- \
  --root "$ROOT" \
  --broker ssl://mqtt.example.com:8883 --ca instance-ca.crt \
  --instance devicechain --tenant acme --device pump-7 --credential-id <credentialId> \
  --origin https://dc.example.com --trust trust.pem
```

`--credential-id` es el id de la credencial, no el nombre de usuario MQTT: el programa envía `{tenant}:{credentialId}` como nombre de usuario y `{instance}:{tenant}:{device}` como id de cliente, como describe [Conexión de un dispositivo](./connecting-a-device.md). `--tls-server-name` verifica el certificado del broker contra un nombre distinto del host del broker, para un clúster local cuyo certificado no cubre `localhost`.

Compila una segunda imagen con una versión superior, declárala como artefacto con el componente `firmware`, fírmalo y asígnalo al dispositivo. Puedes seguir el intento en la pestaña Actualizaciones del dispositivo mientras pasa de `INITIATED` por `DOWNLOADING`, `VERIFIED` y `REBOOTING` hasta `UPDATED`. Si asignas una imagen firmada con una clave que el dispositivo no tiene, el intento termina en `FAILED` con `UNKNOWN_SIGNER`, después de la descarga. Si asignas una cuya versión no es más reciente, el dispositivo la rechaza antes de descargar nada.

## Lo que debe añadir un dispositivo real {#what-a-real-device-must-add}

El ejemplo es honesto sobre lo que no es:

- **Flash a prueba de cortes de corriente.** Un dispositivo real escribe el banco inactivo, lo verifica y cambia un bit de selección de banco que el hardware hace atómico. Aquí lo sustituyen un renombrado y un fsync.
- **Una raíz de confianza en hardware.** Ancla el conjunto de confianza y el lanzador. El lanzador de aquí es un programa, no una ROM.
- **Protección contra reversión.** Un contador anti-reversión guardado en hardware, para que no se pueda forzar una degradación borrando el estado del dispositivo.
- **Una primera ejecución en un entorno aislado.** La comprobación de identidad limita el directorio de trabajo, el entorno y el tiempo, no la red ni el sistema de archivos. Un dispositivo real la ejecuta en un dominio de privilegios mínimo, o verifica un manifiesto en lugar de ejecutar la imagen.
- **Una zona de preparación que nadie más pueda escribir.** La imagen se calcula, se verifica, se ejecuta una vez y luego se renombra; quien pueda escribir la ranura inactiva entre medias burla las comprobaciones.
- **Almacenamiento seguro para la credencial.**
- **Una forma de enterarse de que se revocó un firmante.** La plataforma se niega a asignar un artefacto firmado solo por un firmante revocado, pero un dispositivo actúa según el conjunto de confianza que tiene.

Tampoco hace actualizaciones delta ni imágenes cifradas, ni gestiona configuración, LwM2M o Sparkplug. Se conecta como un dispositivo MQTT ordinario. El orden de comprobaciones anterior es lo que pasa sin cambios a un dispositivo real.

## Relacionado

- **[Actualizaciones over-the-air](../concepts/over-the-air-updates.md)**: asignaciones, intentos y despliegues, desde el lado de la plataforma.
- **[Conexión de un dispositivo](./connecting-a-device.md#over-the-air-updates)**: cada mensaje que el dispositivo envía y recibe.
- **[Firmar actualizaciones](./signing-updates.md)**: crear claves, firmar y rotarlas.
