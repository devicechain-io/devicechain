---
title: Integraciones de entrada
sidebar_position: 12.6
---

import GuideShot from '@site/src/components/GuideShot';

# Integraciones de entrada

Algunos dispositivos nunca se conectan por sí mismos a DeviceChain. Un sensor LoRaWAN habla con una
pasarela, y un **servidor de red** como ChirpStack o The Things Stack reenvía por HTTPS lo que ha
decodificado. Una **integración de entrada** es el extremo receptor de ese reenvío. Da al remitente una
URL y un token de portador, encuentra el dispositivo al que se refiere cada mensaje y guarda las lecturas
como mediciones normales.

Este capítulo es para los **administradores del inquilino** y muestra el lado de la consola: crear una
integración, conectar el remitente, dar a los dispositivos los identificadores que usa el remitente y ver
qué llegó y no se pudo aprovechar. El contrato completo (la URL, los límites, cómo se tratan los
duplicados y los reintentos, y qué se le responde al remitente) está en la
[guía de integraciones de entrada](/guides/inbound-integrations).

**Integraciones** está en **Conectar** en la barra lateral. La página necesita el permiso `device:read`
para abrirse y `device:write` para cambiar algo. Un miembro limitado a algunos de los clientes o grupos
del inquilino no la ve. Cuántas integraciones puede tener un inquilino es un ajuste de su nivel; crear
una por encima del límite se rechaza.

## La lista de integraciones {#the-list}

La lista muestra el nombre, el token y el tipo de remitente de cada integración, los tipos de
dispositivo a los que puede llegar, cuándo falló por última vez la autenticación de una petición suya y
su estado. Tres insignias piden atención:

- **Deshabilitada**: la integración no acepta datos.
- **Token rechazado**: una petición de esta integración ha fallado en la autenticación. Comprueba el
  token que presenta el remitente.
- **N sin decodificar**: llegaron envíos de los que no se podían sacar lecturas. Consulta
  [Envíos sin nada que leer](#undecoded).

## Crear una integración {#creating}

Antes de empezar, ten un tipo de dispositivo para los dispositivos que hay detrás del remitente, y a ser
posible una [métrica](/user-guide/device-types-and-profiles#métricas) declarada para cada valor
decodificado al que quieras dar una unidad. Después elige **Nueva integración** y rellena:

- **Tipo de remitente**: **ChirpStack v4**, **The Things Stack v3**, **JSON genérico** o **Binario
  genérico**. Fija cómo se lee el cuerpo y **no se puede cambiar** después.
- **Token**: parte de la URL a la que publica el remitente. No se puede cambiar después. El token de
  portador con el que se autentica el remitente se genera por ti.
- **Nombre** y **Descripción**.
- **Tipos de dispositivo**: solo se llega por la integración a dispositivos de estos tipos, diga lo que
  diga el remitente. Elige al menos uno.
- **Identificador del dispositivo** (servidores de red): cómo nombra el remitente un dispositivo, por su
  **DevEUI** o, en The Things Stack, por su **Id de dispositivo** en el servidor de red.
- **Guardar métricas de enlace**: guarda la intensidad de señal, la relación señal/ruido y el número de
  pasarelas junto a cada envío decodificado.
- **Aceptando datos**: desactívalo para dejar de resolver dispositivos por esta integración de
  inmediato.

Para un remitente genérico aparece una sección **Dónde están los datos**:

- **Nombre del esquema de identificadores**: el nombre bajo el que se guardan en los dispositivos los
  identificadores del remitente, como el alias `custom.<nombre>`. Usa minúsculas, dígitos, guion y guion
  bajo.
- **El identificador procede de**: **Cabecera**, **Campo del cuerpo** o **Parámetro de consulta**. Un
  cuerpo binario no tiene campos, así que en **Binario genérico** es una cabecera o un parámetro de
  consulta.
- **Ubicación del identificador**: el nombre de la cabecera (como `X-Device-Id`), un puntero JSON al
  campo (como `/device/id`) o el nombre del parámetro de consulta.
- **Matriz de elementos** (opcional): un puntero JSON a una matriz del cuerpo; cada elemento es un
  envío. Déjalo vacío cuando el cuerpo sea un único envío.
- **Hora del remitente** (opcional): un puntero JSON a la hora que registró el remitente. Sin él se usa
  la hora de recepción, y una petición reintentada se guarda dos veces.
- **Trama sin procesar** y **Codificación de la trama** (opcionales, solo JSON): dónde hay una trama sin
  procesar codificada, que se conserva para un decodificador. La trama todavía no se decodifica.

Elige **Crear integración**. El siguiente cuadro, **Integración creada**, muestra el **token de
portador**. **Se muestra una sola vez.** No se puede volver a leer; si se pierde, rótalo. Introdúcelo
ahora en el remitente.

## Conectar el remitente {#connecting}

Selecciona una integración para abrir su página. **Conectar el remitente** enumera lo que hay que
introducir en el remitente, según su tipo, con un botón de copia en cada valor: la URL del endpoint, el
nombre de la cabecera y el valor de la cabecera. El token de portador en sí no se vuelve a mostrar aquí.

<GuideShot id="integration-detail" alt="La página de la integración ChirpStack EU868, token chirpstack-eu868, con los botones Rotar token y Eliminar arriba a la derecha. Un panel Tráfico muestra 0 envíos sin nada que leer y la fecha en que se generó el token. Debajo, un panel Conectar el remitente enumera la codificación JSON, la URL del endpoint de eventos, el nombre de cabecera Authorization y el valor de cabecera 'Bearer <bearer-token>', cada uno con un botón de copia." />

### ChirpStack v4 {#chirpstack}

En ChirpStack, abre la aplicación, añade una integración **HTTP** y rellénala con lo que indica la
página:

- Pon **Payload encoding** en **JSON**. Una integración Protobuf se rechaza con un mensaje que nombra
  el ajuste.
- Introduce la **Event endpoint URL** de la página. ChirpStack añade él mismo el tipo de evento a la
  URL; solo se leen los envíos ascendentes y los demás eventos se confirman y se ignoran.
- Añade la cabecera `Authorization` con el valor `Bearer <token de portador>`.
- Configura un **códec** en el perfil de dispositivo de ChirpStack. Sin él, un envío no lleva valores
  decodificados y se cuenta como sin decodificar.

### The Things Stack v3 {#the-things-stack}

En The Things Stack, abre la aplicación, añade un **Custom webhook** con formato JSON e introduce la
**Base URL** y la cabecera `Authorization` de la página. Habilita el mensaje de envío ascendente en una
de las rutas que enumera la página. Los mensajes en cualquier otra ruta se confirman y se ignoran.
Configura un **payload formatter** en la aplicación o el dispositivo; si no, el envío se cuenta como sin
decodificar.

### Remitentes genéricos {#generic}

Para **JSON genérico** y **Binario genérico**, la página da una petición de ejemplo que muestra dónde va
el identificador. Un remitente genérico debe enviar cada envío como un `POST` HTTP con el token de
portador en la cabecera `Authorization`. Las formas exactas de la petición están en las secciones
[JSON genérico](/guides/inbound-integrations#generic-json) y
[Binario genérico](/guides/inbound-integrations#generic-binary) de la guía. El binario genérico solo
cuenta los envíos y conserva una muestra de los primeros bytes: sirve para ver qué envía un remitente,
no para guardar lecturas.

### El certificado del remitente {#sender-certificate}

Un servidor de red alojado no confiará en una instancia que sirva un certificado autofirmado. Si la tuya
lo hace, pide a quien la gestiona un certificado de una CA pública antes de conectar un remitente
alojado.

## Dar a los dispositivos los identificadores del remitente {#aliases}

Un mensaje nombra un dispositivo como lo conoce el remitente, no como lo conoce DeviceChain. Un **alias**
de dispositivo registra ese nombre en el dispositivo. Abre el formulario del dispositivo y usa el campo
**Alias**: elige **Añadir alias**, selecciona **DevEUI LoRaWAN**, **Id de dispositivo del servidor de
red** o **Esquema personalizado** y escribe el valor. Un dispositivo tiene hasta ocho alias, un valor por
esquema, y un esquema con su valor solo puede pertenecer a un dispositivo del inquilino. El DevEUI se
normaliza al guardarlo, así que da igual mayúsculas o minúsculas.

El **Identificador del dispositivo** de la integración decide qué esquema se consulta: un DevEUI lee el
alias `lorawan.deveui`, un id de dispositivo del servidor de red lee `tts.device-id` y una integración
genérica lee `custom.<nombre>`. Un dispositivo que solo tiene otro esquema no se encuentra. Los alias se
describen por completo en
[Identificar dispositivos con alias](/guides/inbound-integrations#aliases).

## Dispositivos no registrados vistos {#unregistered}

No hace falta crear antes los dispositivos. En la página de la integración, **Dispositivos sin registrar
vistos** enumera los identificadores que recibió la integración y que ningún dispositivo de su ámbito
tiene, con su esquema, el identificador, cuántas veces se vio y cuándo fue la última vez. Junto a cada
uno, **Crear un dispositivo con este alias** abre el formulario del dispositivo con el alias rellenado;
cuando el dispositivo existe, sus datos empiezan a guardarse.

La lista es solo de observación. **Vaciar lista** olvida todo lo que contiene, y un identificador que el
remitente sigue enviando vuelve a aparecer.

## Tráfico y problemas {#traffic}

El panel **Tráfico** de la página de la integración muestra lo que ha recibido y no ha podido usar:
cuántos envíos no tenían nada que leer, cuándo llegó el último, una muestra de su última trama en
hexadecimal, cuándo se rechazó la última petición y cuándo se generó el token.

### Envíos sin nada que leer {#undecoded}

Un envío cuenta como sin decodificar cuando no lleva ningún objeto decodificado con un valor numérico.
En un servidor de red, la causa habitual es que falta un códec o un payload formatter: configura uno en
el perfil de dispositivo de allí y usa codificación JSON. DeviceChain no decodifica la trama sin
procesar.

### Un token rechazado {#rejected-token}

**Token rechazado** significa que una petición falló en la autenticación. Puede que el remitente tenga un
token antiguo, por ejemplo tras una rotación. Comprueba el valor de la cabecera, o rota el token e
introduce el nuevo.

## Rotar el token y eliminar {#rotating}

- **Rotar token** sustituye el token de portador. El anterior deja de funcionar de inmediato, sin
  solapamiento, así que actualiza el remitente enseguida. El nuevo se muestra una sola vez.
- **Eliminar** quita la integración. El remitente se rechaza de inmediato. Los alias de los dispositivos
  se conservan.

## Relacionado

- [Guía de integraciones de entrada](/guides/inbound-integrations): la URL, los límites, los duplicados y
  reintentos, y la resolución de problemas.
- [Dispositivos](/user-guide/devices): el formulario del dispositivo y sus alias.
- [Tipos de dispositivo y perfiles](/user-guide/device-types-and-profiles): declarar las métricas bajo
  las que se guarda un valor decodificado.
- [Aprovisionamiento y anclas de confianza](/user-guide/provisioning-and-trust-anchors): dispositivos que
  se conectan por sí mismos.
