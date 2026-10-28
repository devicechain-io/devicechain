---
title: Actualizaciones (por aire)
sidebar_position: 16
---

import GuideShot from '@site/src/components/GuideShot';

# Actualizaciones (por aire)

El área **Actualizaciones** es donde entregas nuevo firmware, software o configuración a tus
dispositivos por aire. Subes una imagen de actualización (un **artefacto**), la avalas con la firma de
una clave en la que confías (un **firmante**) y la asignas a un dispositivo. El dispositivo la descarga
y la instala, y la consola sigue cada paso hasta que el dispositivo demuestra que ejecuta la versión
nueva.

Este capítulo es para los administradores de un inquilino y para quienes publican el software de los
dispositivos. Cubre las pantallas de la consola; para saber cómo funcionan las actualizaciones de
principio a fin, consulta [actualizaciones por aire](../concepts/over-the-air-updates.md), y para
crear claves y firmar artefactos, consulta [firma de actualizaciones](../guides/signing-updates.md).

El servicio de actualizaciones es opcional y no todas las instalaciones lo ejecutan. Si la tuya no lo
hace, la barra lateral no tiene el grupo **Actualizaciones** y los dispositivos no tienen la pestaña
Actualizaciones. Consulta [cómo activarlo](../concepts/over-the-air-updates.md#enabling).

En la barra lateral, **Dispositivos → Actualizaciones** tiene tres pantallas: **Artefactos**, **Despliegues** y **Firmantes**.
Actualizar un grupo de dispositivos completo se hace en [Despliegues](#rollouts). La
asignación de una actualización a un dispositivo se hace en la página del propio dispositivo, en su
[pestaña Actualizaciones](#assigning-an-update-to-a-device).

Algunos textos de esta área aparecen en inglés en la consola, aunque uses el español: por ejemplo, el
título **Updates** de la cabecera, los estados de una actualización como **QUEUED** o el de un firmante
como **ACTIVE** en la pestaña del dispositivo, y los nombres de los datos de procedencia de un
artefacto. Esta guía los cita tal como aparecen.

## Artefactos

Un artefacto es una imagen de actualización: un archivo más lo que es (su tipo, nombre, versión y
componente) y con qué perfiles de dispositivo es compatible. Una vez subido su contenido, un
artefacto no se puede cambiar.

<GuideShot id="updates-artifacts-list" alt="La lista Artefactos con tres imágenes de firmware de termostato: 2.5.0-rc.1, Sin verificar y Disponible; 2.4.0, Firmado y Disponible; y 2.3.1, Firmado y Retirado. Cada fila muestra el componente, el tamaño y el principio de la huella SHA-256. Arriba están los filtros y el botón Nuevo artefacto." />

Cada fila muestra el nombre, la versión, el componente, la **Confianza** (Firmado o Sin verificar), el
**Estado**, el tamaño y la **Huella** SHA-256. Los estados son:

- **Subiendo**: el artefacto está declarado, pero su contenido aún no está almacenado.
- **Disponible**: el contenido está almacenado y verificado, así que el artefacto se puede asignar.
- **Retirado**: el artefacto queda fuera de uso para nuevas asignaciones. Sus metadatos y su contenido
  permanecen.

Los selectores **Cualquier confianza** y **Cualquier estado** filtran todo el catálogo. El cuadro
**Filtrar esta página…** es distinto: solo acota la página ya cargada, por nombre, versión,
componente, token o huella. Para buscar más allá, usa **Siguiente**.

### Añadir un artefacto

Haz clic en **Nuevo artefacto**. Empiezas por el archivo y la consola hace el resto de una vez:
calcula el tamaño y el SHA-256 del archivo en tu navegador, declara el artefacto y sube el contenido.

<GuideShot id="updates-artifact-new" alt="El panel Nuevo artefacto. Un botón Elegir archivo con la nota Máximo aceptado: 512 MiB, y después los campos Tipo, Nombre, Versión, Componente, Token, Perfiles de dispositivo compatibles y Tipo de medio." />

1. Haz clic en **Elegir archivo**. Bajo el botón se muestra el archivo más grande que acepta tu
   instancia; uno mayor se rechaza antes de enviar nada.
2. Elige el **Tipo**: Firmware, Software o Configuración.
3. Escribe el **Nombre**, la **Versión** y el **Componente**. La versión es una etiqueta que la
   plataforma no interpreta. El componente es la parte del dispositivo que actualiza, como `firmware`
   o `app/agent`. Nombre, componente y versión deben ser únicos en conjunto.
4. El **Token** se rellena a partir del nombre y la versión. Puedes cambiarlo.
5. Elige los **Perfiles de dispositivo compatibles**. El artefacto solo se puede asignar a
   dispositivos con esos perfiles, así que elige al menos uno.
6. Si quieres, indica el **Tipo de medio** (vacío, es `application/octet-stream`), un **Origen** y
   unas **Notas**.
7. Haz clic en **Declarar y subir**.

La subida muestra su progreso. Al terminar, la consola compara lo que almacenó el servicio con lo que
declaraste. **Contenido almacenado y verificado** significa que coinciden. Si no coinciden, la consola
lo indica y el artefacto sigue sin estar disponible: revisa el archivo y vuelve a subirlo.

Si la subida falla después de declarar el artefacto, el artefacto existe pero no tiene contenido.
Haz clic en **Reintentar la subida**, o en **Abrir artefacto** y sube el archivo desde su página.

### La página del artefacto

Abre un artefacto para ver su página. La cabecera muestra su nombre y versión, su token e insignias
con su confianza, su estado y su tipo. **Retirar** deja el artefacto fuera de uso para nuevas
asignaciones, tras confirmarlo. Retirarlo no se puede deshacer.

<GuideShot id="updates-artifact-signed" alt="La página de Thermostat firmware 2.4.0, con las insignias Firmado, Disponible y FIRMWARE y un botón Retirar. El panel Detalles lista el componente, la versión, el tamaño, el tipo de medio, la huella, las fechas de declaración y último cambio, el perfil compatible bp-thermostat-profile y las entradas de procedencia notes, source y uploadedBy. Debajo, el panel Firmas y confianza muestra la insignia Firmado y una firma, de Acme release signing key." />

El panel **Detalles** lista lo que se declaró, con la huella completa tras el botón de copiar. A
continuación aparece la procedencia registrada con el artefacto, como sus notas y su origen. Esas
entradas se muestran con las claves con las que se guardaron, sin traducir (por ejemplo
**UPLOADEDBY**).

Un artefacto que sigue en estado **Subiendo** tiene un panel **Subir contenido**. Elige el archivo
declarado para el artefacto; la consola comprueba su tamaño y su SHA-256 antes de enviar nada y
rechaza un archivo que no sea el declarado.

### Firmas y confianza

La confianza de un artefacto se calcula cada vez que se lee, a partir de las firmas registradas y del
estado de los firmantes que las respaldan:

- **Firmado**: al menos un firmante activo tiene una firma verificada sobre el artefacto.
- **Sin verificar**: ningún firmante activo la tiene. La plataforma sigue comprobando que los bytes
  son los declarados, pero no dice nada de quién los creó.

El panel **Firmas y confianza** muestra la confianza y luego lista las firmas registradas sobre este
artefacto, de la más antigua a la más reciente. De cada una ves el nombre del firmante, su ID de
clave, el algoritmo, el estado del firmante, cuándo se añadió la firma y quién la añadió, y si sigue
contando: **Cuenta para la confianza**. La firma de un firmante revocado después sigue registrada,
pero indica **Ya no cuenta (firmante revocado)**. Si el artefacto no tiene firmas, el panel lo dice.

<GuideShot id="updates-artifact-signatures" alt="La parte inferior de la página de Thermostat firmware 2.4.0: el panel Firmas y confianza con la insignia Firmado y una tabla con una firma, de Acme release signing key: su ID de clave, el algoritmo ED25519, el estado Activo, cuándo y quién la añadió, y Cuenta para la confianza. Debajo, el formulario Añadir una firma con los campos ID de clave del firmante y Firma (base64)." />

Para añadir una firma, rellena **Añadir una firma** al final del panel:

- **ID de clave del firmante**: el ID de clave de un firmante registrado, como aparece en la pantalla
  Firmantes o lo imprime tu herramienta de firma. La consola te dice con qué firmante coincide, o te
  avisa si está revocado o no existe.
- **Firma (base64)**: la firma sobre la huella del artefacto. Una clave Ed25519 firma los 32 bytes sin
  procesar de la huella; una firma ECDSA P-256 es compatible con `cosign sign-blob`.

Haz clic en **Añadir firma**. El servicio verifica la firma contra la huella almacenada del artefacto
y rechaza la que no se verifica. La [guía de firma](../guides/signing-updates.md) muestra los comandos
de cada paso.

### Artefactos sin verificar

Un artefacto sin verificar lleva un aviso en lo alto de su página. Te dice qué permite tu instancia:
normalmente, un artefacto sin verificar **no se puede asignar** a un dispositivo hasta que se firme.
Una instancia se puede configurar para permitirlo, pero eso está pensado solo para desarrollo. Si la
consola no pudo leer ese ajuste, el aviso te dice que supongas que el artefacto no se puede asignar.

<GuideShot id="updates-artifact-unverified" alt="La página de Thermostat firmware 2.5.0-rc.1, con las insignias Sin verificar, Disponible y FIRMWARE. Un aviso indica que este artefacto no está verificado y que esta instancia no permite asignar artefactos sin verificar, así que no se puede asignar a un dispositivo hasta que se firme. Enlaza a la guía de firma." />

## Firmantes

Un **firmante** es una clave pública en la que tu inquilino confía para avalar artefactos. Solo se
guarda la clave pública; la clave privada la conserva quien firma. Abre
**Dispositivos → Actualizaciones → Firmantes**.

<GuideShot id="updates-signers-list" alt="La lista Firmantes con dos firmantes: Acme release signing key, ED25519, Activo, con 2 artefactos con firma única y un botón Revocar; y Contract manufacturer key (2025), ECDSA_P256, Revocado, con su fecha de revocación y 0 artefactos con firma única." />

La lista muestra el nombre, el ID de clave, el algoritmo y el estado de cada firmante, cuándo se
registró y se revocó, y sus **Artefactos con firma única**: cuántos artefactos no tienen ningún otro
firmante activo y, por tanto, pasarían a sin verificar si se revocara este.

Registrar y revocar firmantes requiere el permiso `artifact:trust`, que es independiente del permiso
para subir artefactos. Sin él, ves la lista pero ninguna acción.

### Registrar un firmante

Haz clic en **Registrar firmante** e indica:

- **Nombre**: una etiqueta para reconocer al firmante, como el equipo o el pipeline de compilación que
  posee la clave.
- **Clave pública (PEM)**: un bloque de clave pública SPKI, ECDSA P-256 o Ed25519.

<GuideShot id="updates-signer-register" alt="El panel Registrar firmante con el campo Nombre, el campo Clave pública (PEM) con las líneas BEGIN y END PUBLIC KEY, un enlace sobre cómo generar una clave y calcular su ID de clave, y el botón Registrar." />

Haz clic en **Registrar**. Pega solo la mitad pública del par de claves. Si pegas algo que parece una
clave privada, la consola no envía nada y vacía el campo. El enlace del panel lleva a la
[guía de firma](../guides/signing-updates.md), que explica cómo generar una clave y calcular su ID de
clave.

### Revocar un firmante

Haz clic en **Revocar** en un firmante activo. La confirmación te dice cuántos artefactos no tienen
otro firmante activo y pasarán a **SIN VERIFICAR**. Ese recuento incluye los artefactos retirados.

<GuideShot id="updates-signer-revoke" alt="El diálogo de confirmación para revocar Acme release signing key. Indica que 2 artefactos no tienen otro firmante activo y pasan a sin verificar, y que las asignaciones de actualización ya enviadas conservan la firma con la que se fijaron. Botones Cancelar y Revocar firmante." />

La revocación es definitiva. Las firmas del firmante siguen registradas pero dejan de contar, así que
los artefactos que solo él avalaba pasan a sin verificar de inmediato. Una actualización ya asignada
conserva la firma con la que se asignó, de modo que se sigue entregando tal como se asignó. Consulta
[revocar un firmante](../guides/signing-updates.md#revocar-un-firmante).

## Asignar una actualización a un dispositivo {#assigning-an-update-to-a-device}

Las actualizaciones se asignan de dispositivo en dispositivo, desde la página del dispositivo. Abre el
dispositivo y elige la pestaña **Actualizaciones**. La pestaña solo aparece si tu instancia ejecuta el
servicio de actualizaciones y tienes permiso para leer actualizaciones.

<GuideShot id="updates-device-tab" alt="La pestaña Actualizaciones del dispositivo Lobby thermostat. En Actualización actual, Thermostat firmware 2.4.0 está Firmado y QUEUED, con su componente, el ID del intento, el progreso No informado, la hora del último informe, quién la asignó, Requiere reinicio Sí y el firmante fijado Acme release signing key, ACTIVE. Le sigue el botón Cancelar actualización. Debajo, la sección Asignar una actualización tiene un selector de artefacto, una nota de que el dispositivo ya tiene una actualización en curso y el botón Asignar desactivado." />

Los enlaces de arriba llevan a la guía de firma y a cómo funcionan los firmantes. **Actualizar** vuelve
a cargar la pestaña.

### Actualización actual

La tarjeta **Actualización actual** muestra la actualización en la que trabaja el dispositivo: el
artefacto y su confianza, el estado del intento de actualización, el componente, el ID del intento,
el progreso, cuándo informó el dispositivo por última vez, quién la asignó y si requiere reinicio.
**Firmante fijado** muestra la firma que se envió al dispositivo con la actualización. Si ese firmante
se revoca después, la tarjeta lo indica: la actualización conserva la firma con la que se asignó.

La insignia de estado sigue lo que informa el dispositivo, desde **QUEUED**, pasando por la descarga,
la verificación, la instalación y el reinicio, hasta **UPDATED**. La actualización solo llega a
**UPDATED** con pruebas: el dispositivo informa de que ejecuta la versión nueva. La lista completa de
estados está en [cómo avanza un intento](../concepts/over-the-air-updates.md#states).

**QUEUED** significa que la plataforma ha creado la actualización, pero el dispositivo todavía no ha
informado nada sobre ella. El progreso indica **No informado** hasta que lo haga. En la captura, la
actualización se queda en QUEUED porque el termostato simulado de este inquilino de demostración
nunca informa del progreso de una actualización. Un dispositivo real que recibe la asignación la
confirma y la hace avanzar.

Si la actualización falla, la tarjeta muestra por qué: la etapa en la que falló, si el fallo lo
informó el dispositivo o lo decidió la plataforma y, para los fallos habituales de firma y de
descarga, qué hacer al respecto.

### Cancelar o abandonar una actualización

- **Cancelar actualización** se ofrece hasta que empieza la instalación. Una actualización en
  **QUEUED** se cancela de inmediato, y después se puede asignar otra. Si el dispositivo ya ha recogido
  la actualización, cancelar le envía una petición para que se detenga: la cancelación solo se
  completa cuando el dispositivo la confirma, y hasta entonces la tarjeta muestra **Cancelación
  pendiente**. Una vez iniciada la instalación, la actualización ya no se puede cancelar.
- **Abandonar actualización** se ofrece cuando una actualización está en estado desconocido: el
  dispositivo dejó de informar mientras instalaba o reiniciaba, así que nadie sabe qué versión
  ejecuta. Abandonarla la termina como cancelada, guarda en ella el **Motivo** que indiques (es
  obligatorio), registra el cambio a tu nombre, envía una cancelación al dispositivo y lo libera para una nueva asignación. No averigua
  qué ejecuta el dispositivo, así que revisa su inventario antes de dar por buena cualquiera de las dos
  versiones.

### Asignar una actualización

Usa **Asignar una actualización**:

1. Elige un **Artefacto**. Solo se listan artefactos disponibles, y el perfil del dispositivo debe
   estar en la lista de compatibles del artefacto. Un artefacto sin verificar aparece como **No
   asignable** cuando tu instancia no permite artefactos sin verificar.
2. Haz clic en **Asignar**.

Si el dispositivo ya tiene una actualización en curso, la sección lo indica, y asignar otra se
rechaza hasta que esa se cancele o termine. Cuando la instancia no permite artefactos sin verificar,
**Asignar** sigue desactivado para uno sin verificar, y se muestra el motivo.

### Historial de asignaciones

**Historial de asignaciones** lista las asignaciones pasadas y la actual del dispositivo, con el
artefacto, el estado, el fallo si lo hubo, cuándo se asignó cada una y quién la asignó.

### Un dispositivo deshabilitado

Cuando un dispositivo está deshabilitado, la pestaña indica que no recibe actualizaciones. No se puede
asignar ninguna actualización nueva hasta que se vuelva a habilitar, y **Asignar** está desactivado.
Deshabilitar el dispositivo no revoca una actualización ya en curso; cancélala en esta pestaña si no
debe continuar. Una actualización en cola se cancela de inmediato. Una que el dispositivo ya ha
recogido queda en **Cancelación pendiente** mientras el dispositivo esté deshabilitado, porque se
rechazan sus informes: la cancelación se completa cuando el dispositivo se vuelve a habilitar y la
confirma, o la actualización termina por tiempo agotado al vencer el plazo de su etapa.

## Despliegues {#rollouts}

**Despliegues** actualiza un grupo de dispositivos completo, unos pocos dispositivos a la vez. Úsalo
cuando un dispositivo cada vez sea demasiado lento y todos a la vez sea demasiado arriesgado.

**Nuevo despliegue** pide un token, un nombre opcional, un **Artefacto** (solo se ofrecen los
disponibles), un **Grupo de dispositivos**, **Dispositivos actualizándose a la vez** y un **Umbral de
fallos** opcional. El despliegue se crea como borrador y todavía no se envía nada a ningún dispositivo.

En la página del despliegue:

- **Iniciar** fija los miembros actuales del grupo como dispositivos del despliegue y da a los primeros
  la orden de actualizar. Los dispositivos que se unan al grupo después no se incluyen; los que salgan
  siguen en el despliegue.
- **Pausar** hace que ningún dispositivo más reciba la orden. Los que ya se están actualizando
  continúan, y **Reanudar** sigue desde donde se detuvo.
- **Abortar** cancela los dispositivos que aún no recibieron la orden y termina el despliegue. Los que
  ya se están actualizando terminan; puedes cancelar cualquiera de ellos desde su propia
  [pestaña Actualizaciones](#assigning-an-update-to-a-device).

El resumen muestra cuántos dispositivos están pendientes, en curso, correctos, fallidos,
desconocidos, cancelados y omitidos, y la tabla **Dispositivos** muestra cada uno con su resultado,
filtrable por resultado. Un dispositivo omitido fue rechazado por las mismas reglas que una asignación
individual (por ejemplo, un dispositivo deshabilitado o uno ocupado con otra actualización), y su
columna **Motivo** indica cuál. Un dispositivo con resultado desconocido cuenta como fallo y conserva
su plaza dentro de **Dispositivos actualizándose a la vez**.

Cuando los dispositivos fallidos y desconocidos alcanzan el **Umbral de fallos**, el despliegue se
pausa solo. Para continuar, introduce un umbral más alto y elige **Reanudar con este umbral**:
reanudar nunca pone el recuento a cero. Un despliegue también se pausa solo si su artefacto se retira
mientras está en curso.

Los despliegues necesitan `artifact:read` para verlos y `artifact:write` para crearlos y controlarlos.

## Miembros limitados a algunos clientes {#members-limited-to-some-customers}

Un miembro cuyo acceso está limitado a algunos de tus clientes, y que tiene `artifact:read`, puede leer
las asignaciones de actualización de los dispositivos que estaban asignados a uno de esos clientes
cuando se les asignó la actualización. No puede asignar, cancelar ni abandonar una actualización, ni
listar artefactos, firmantes o despliegues. Las asignaciones hechas antes de que tu instancia registrara a quién
estaba asignado un dispositivo no se muestran a ese miembro.

## Relacionado

- [Actualizaciones por aire](../concepts/over-the-air-updates.md): artefactos, asignaciones, estados
  de una actualización, plazos y firmantes.
- [Firma de actualizaciones](../guides/signing-updates.md): generar una clave, registrarla, firmar un
  artefacto y rotar claves.
- [Construir un dispositivo actualizable](/guides/building-an-updatable-device): un dispositivo de
  referencia que verifica, instala y revierte una actualización.
