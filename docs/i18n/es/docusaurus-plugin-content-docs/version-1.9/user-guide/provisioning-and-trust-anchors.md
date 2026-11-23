---
title: Aprovisionamiento y anclas de confianza
sidebar_position: 12.5
---

import GuideShot from '@site/src/components/GuideShot';

# Aprovisionamiento y anclas de confianza

Este capítulo es para los **administradores del inquilino** que incorporan flotas de dispositivos a
DeviceChain sin crear ni configurar cada dispositivo a mano. Cubre tres cosas que están en
**Conectar** en la barra lateral y en la página del dispositivo:

- **Perfiles de aprovisionamiento**: el punto de entrada compartido por el que se registra una flota.
- **Anclas de confianza**: las autoridades de certificación en las que tu inquilino confía para avalar
  los certificados de los dispositivos.
- **Credenciales de certificado**: un dispositivo que inicia sesión con un certificado de cliente en
  lugar de un token o una contraseña.

Ambas páginas necesitan el permiso `device:read` para abrirse y `device:write` para cambiar algo. Un
miembro limitado a algunos de los clientes o grupos del inquilino no las ve.

:::note Qué se distribuye hoy
La autenticación por certificado llega por etapas. En la consola puedes crear anclas de confianza,
añadir credenciales de certificado a los dispositivos y crear perfiles de aprovisionamiento por
certificado. Un dispositivo cuyo certificado está registrado como credencial puede conectarse con él
al broker MQTT, y un dispositivo cuyo certificado encadena con el ancla de confianza de un perfil de
certificado se registra solo en su primera conexión. Adoptar un dispositivo que creaste de antemano y
renovar un certificado todavía no están disponibles. Consulta
[Qué funciona hoy con certificados](#certificates-today).
:::

## Perfiles de aprovisionamiento {#provisioning-profiles}

Un **perfil de aprovisionamiento** permite que una flota se registre sola. Cada unidad sale de la
línea con la misma clave y el mismo secreto, y se presenta la primera vez que se enciende; no creas
antes cada dispositivo. Este capítulo es el lado de la consola. Cómo hace la petición el firmware y
qué respuestas puede recibir está en [Autorregistro de dispositivos](/guides/device-self-registration).

Abre **Conectar → Perfiles de aprovisionamiento**. La lista muestra el nombre, el token, el tipo de
dispositivo y la estrategia de cada perfil, cuántos dispositivos ha registrado (frente a su límite, si
lo tiene) y su estado, con insignias para un perfil **Deshabilitado**, **Caducado** o marcado como
**Rotación necesaria**.

### Crear un perfil {#creating-a-profile}

Elige **Nuevo perfil de aprovisionamiento** y rellena:

- **Nombre** y **Descripción**, como en cualquier otra entidad.
- **Se autentica mediante**: **Secreto compartido** o **Certificado** (consulta
  [Perfiles de aprovisionamiento por certificado](#certificate-profiles)). Se elige al crear el perfil.
- **Clave de aprovisionamiento** (solo secreto compartido): el nombre público con el que se registran
  los dispositivos. Es única en el inquilino y se guarda tal como la escribes. No es secreta.
- **Tipo de dispositivo**: el tipo de todos los dispositivos que crea el perfil, y el único que
  adoptará.
- **Estrategia**: **Permitir dispositivos nuevos** crea un dispositivo que la plataforma no conocía y
  también adopta uno que esperabas para este perfil. **Solo dispositivos preregistrados** registra
  únicamente los dispositivos que creaste antes y esperabas para este perfil; un dispositivo
  desconocido se rechaza.
- **Límite de dispositivos**: el máximo de dispositivos que este perfil puede registrar. Déjalo vacío
  para no tener límite. Bajarlo solo rechaza nuevos registros; no elimina nada.
- **Caduca**: a partir de ese momento el perfil rechaza nuevos registros. Los dispositivos ya
  registrados no se ven afectados. Déjalo vacío para que nunca caduque.
- **Aceptando registros**: desactívalo para rechazar nuevos registros de inmediato.

Elige **Crear perfil de aprovisionamiento**. En un perfil de secreto compartido, el siguiente cuadro
es **Copia ahora el secreto de aprovisionamiento**. Muestra la clave y el secreto que generó el
servidor. **El secreto se muestra una sola vez.** La plataforma guarda solo un resumen unidireccional,
así que no puede mostrarlo de nuevo; si lo pierdes, rótalo. Graba la clave y el secreto en el firmware
de la flota.

### Seguir un perfil {#following-a-profile}

Selecciona un perfil para abrirlo. El panel **Registro** cuenta los **Dispositivos registrados** (los
que tienen una credencial emitida por este perfil, incluidos los revocados) y los **Dispositivos
esperados para este perfil** (dispositivos creados para él que no necesariamente se han registrado).
**Ver dispositivos** abre la lista de dispositivos filtrada por cualquiera de los dos conjuntos.

Desde la página del perfil también puedes:

- **Rotar secreto**: sustituye el secreto. El anterior deja de funcionar de inmediato, sin
  solapamiento, y el nuevo se muestra una sola vez. Los dispositivos ya registrados conservan sus
  credenciales. Un perfil cuyo secreto se eligió antes de que la plataforma generara secretos se marca
  como **Rotación necesaria** y rechaza todos los registros hasta que lo rotes.
- **Revocar dispositivos**: deshabilita las credenciales de los dispositivos registrados mediante el
  perfil y cierra sus conexiones activas. Se rechazan hasta que restablezcas el aprovisionamiento de
  cada uno. Hazlo, y rota el secreto, si el secreto pudo filtrarse.
- **Eliminar**: se rechaza mientras algún dispositivo conserve una credencial emitida por el perfil.
  Primero revoca los dispositivos y luego restablece su aprovisionamiento o elimínalos.

### Esperar un dispositivo {#expecting-a-device}

Un perfil adopta un dispositivo existente solo si lo esperabas para ese perfil. Indica **Perfil de
aprovisionamiento esperado** al crear el dispositivo, en el formulario del dispositivo; la lista ofrece
los perfiles del tipo del dispositivo. Solo se puede indicar al crear el dispositivo. Los motivos, y los
equivalentes en CSV y API, están en [Marcado previo](/guides/device-self-registration#earmarking).

La página de un dispositivo muestra cómo llegó a la plataforma y ofrece **Restablecer
aprovisionamiento**. Úsalo para que un dispositivo revocado, o uno que perdió su credencial, pueda
registrarse de nuevo.

## Anclas de confianza {#trust-anchors}

Un **ancla de confianza** es el certificado de una autoridad de certificación (CA). Un certificado de
dispositivo solo se acepta si su cadena llega a un ancla de confianza que hayas registrado. Abre
**Conectar → Anclas de confianza**. La lista muestra el sujeto y la caducidad de cada ancla y cuántas
credenciales y perfiles la usan, con insignias para un ancla **Deshabilitada**, **Caducada** o que
**Caduca en N d**.

<GuideShot id="trust-anchors-list" alt="La página Anclas de confianza. Su descripción dice que son las autoridades de certificación en las que este inquilino confía para avalar los certificados de cliente de sus dispositivos. Arriba a la derecha hay un botón Nueva ancla de confianza. La tabla tiene una fila: Acme device CA, token acme-device-ca, sujeto CN=Acme Device CA,O=Acme Properties, una fecha de caducidad, En uso 'Credenciales 0 · Perfiles 0' y una insignia verde Habilitada." />

### Añadir un ancla {#adding-an-anchor}

Elige **Nueva ancla de confianza**, ponle un token y un nombre, y aporta el **Certificado de la CA
(PEM)**: pégalo o elige el archivo.

- Aporta exactamente un certificado. Puede ser una raíz o una intermedia, si quieres confiar solo en
  una sub-CA.
- Aporta solo el certificado, **nunca una clave privada**. El formulario rechaza el texto que contenga
  una.
- Un certificado no se puede cambiar después. Otra CA es otra ancla.

Un inquilino puede tener hasta 32 anclas de confianza. Si el servidor rechaza el certificado, el
formulario explica el motivo en una frase, por ejemplo que no es un certificado.

### Ver, deshabilitar y eliminar un ancla {#managing-an-anchor}

Selecciona un ancla para ver su huella SHA-256, el sujeto, el emisor, las fechas entre las que es válida
y un panel **En uso** con el número de credenciales y perfiles de aprovisionamiento que la nombran.

- **Deshabilitar** deja de confiar en el ancla. La consola te avisa antes: todos los dispositivos cuyo
  certificado encadena con ella dejan de autenticarse y se **desconectan ahora**, y no pueden volver a
  conectarse hasta que habilites el ancla de nuevo.
- **Habilitar** lo revierte.
- **Eliminar** se rechaza mientras una credencial o un perfil siga nombrando el ancla. La consola
  explica los recuentos. Quítalos o redirígelos primero, o deshabilita el ancla para dejar de confiar
  en ella.

## Credenciales de certificado en un dispositivo {#certificate-credentials}

Un dispositivo puede tener una credencial que sea un certificado. Abre el dispositivo y su pestaña
**Credenciales** (consulta [Credenciales](/user-guide/devices#credentials)). En **Tipo** elige
**X509_CERTIFICATE** y:

1. Pega el **Certificado del dispositivo (PEM)**. Es el certificado propio del dispositivo, seguido
   opcionalmente de los certificados intermedios que lo enlazan con el ancla de confianza. Solo
   certificados, nunca una clave privada.
2. Selecciona el **Ancla de confianza** a la que debe llegar la cadena. Solo se ofrecen las anclas
   habilitadas y no caducadas.
3. Elige **Añadir credencial de certificado**.

La plataforma verifica la cadena en ese momento y rechaza un certificado cuya cadena no podría
autenticar. Después calcula el **pin SHA-256** del certificado, que pasa a ser el id de la credencial,
y lo muestra como **Certificado fijado**, con el nombre común y la fecha hasta la que es válido. El pin
es público; identifica el certificado y no es un secreto.

Cada fila de la pestaña muestra el nombre común, el pin, el ancla y la primera caducidad de todo el
certificado y su cadena, señalada cuando está próxima o vencida. Un dispositivo puede tener como máximo
**tres** credenciales de certificado; la consola lo indica antes de que intentes añadir una cuarta. Una
credencial de certificado sin ancla de confianza no puede autenticar; quítala y añádela de nuevo.

## Perfiles de aprovisionamiento por certificado {#certificate-profiles}

Un perfil cuyo **Se autentica mediante** es **Certificado** nombra un ancla de confianza en lugar de
tener un secreto. No hay nada que emitir, mostrar ni rotar. El token del dispositivo se deriva de su
certificado: `pv-` seguido del nombre común del certificado, que es el id externo del dispositivo.

Esa regla se ve donde esperas un dispositivo para un perfil así. En el formulario del dispositivo,
indica como **Id externo** el nombre común del certificado y deja el token vacío, o indica como token
el valor derivado. Un dispositivo esperado para un perfil por certificado no puede cambiar después su
id externo. Un inquilino puede tener hasta ocho perfiles por certificado.

## Qué funciona hoy con certificados {#certificates-today}

- **Un certificado registrado se conecta.** Un dispositivo se conecta al broker MQTT por TLS y presenta
  su certificado de cliente. Su nombre de usuario MQTT es `{tenant}:@x509`, sin contraseña, y su id de
  cliente es el que la plataforma asigna a ese dispositivo. La plataforma busca el pin del certificado,
  comprueba la cadena frente al ancla de confianza de la credencial en cada conexión y termina la sesión
  a más tardar en la primera caducidad de la cadena. Deshabilitar una credencial, un ancla o el
  dispositivo rechaza la siguiente conexión.
- **El registro en la primera conexión funciona.** Un certificado que ninguna credencial fija, y que
  encadena con el ancla de confianza de un perfil de certificado habilitado con la estrategia **Permitir
  dispositivos nuevos**, crea el dispositivo y le fija el certificado. El token del dispositivo se deriva
  de su nombre común, así que su client id se calcula a partir del certificado. Si un certificado
  encadena con las anclas de más de un perfil, gana el perfil cuyo ancla está más cerca del certificado,
  y un empate se rechaza. Los pasos y la regla del client id están en
  [Conectar con un certificado](/guides/device-x509#registration).
- **Adoptar y renovar todavía no están disponibles.** Un dispositivo que creaste de antemano y reservaste
  para un perfil de certificado no puede registrarse por certificado, ni tampoco uno cuyo aprovisionamiento
  se restableció; dale tú una credencial de certificado. Un dispositivo que ya tiene un certificado y
  presenta otro distinto se rechaza hasta que llegue la renovación. Añade el certificado nuevo como
  credencial antes de que caduque el anterior.
- **Necesita el broker de DeviceChain.** El broker predeterminado pide a cada dispositivo un certificado
  de cliente. Una instancia devuelta a la imagen del broker original sigue atendiendo a los dispositivos
  con token y contraseña y rechaza los de certificado. Consulta
  [Versiones y actualizaciones](/deployment/releases-and-upgrades).
- **Vigilarlo.** La alerta `DeviceX509ChainFailuresSustained` y las relacionadas se describen en
  [Observabilidad](/deployment/observability).

## Relacionado

- [Autorregistro de dispositivos](/guides/device-self-registration): el lado del firmware, los códigos de
  error y cómo recuperarse de un secreto filtrado.
- [Credenciales de dispositivo](/guides/device-credentials)
- [Dispositivos](/user-guide/devices): la página del dispositivo y su pestaña Credenciales.
- [Integraciones de entrada](/user-guide/inbound-integrations): remitentes que no son dispositivos.
- [Auditoría](/user-guide/audit): los registros y los cambios de credenciales quedan anotados.
