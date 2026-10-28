---
title: Identidad por certificado
---

# Identidad por certificado {#device-certificates}

Un dispositivo puede demostrar quién es con un **certificado de cliente** en lugar de un token o una contraseña. Presenta el certificado en el handshake TLS de su conexión MQTT, y la plataforma comprueba que lo emitió una autoridad certificadora (CA) en la que el tenant confía. No hay ningún secreto compartido que grabar en el firmware, y nada de lo que guarda el dispositivo puede reproducir alguien que copie un mensaje de la red: la clave privada nunca sale del dispositivo.

Esta página explica cómo decide la plataforma confiar en un certificado y dónde se detiene esa confianza. Para configurarlo, sigue [Conectar con un certificado](../guides/device-x509.md). Para operar el broker y vigilarlo, consulta [Autenticación por certificado](../deployment/device-certificates.md).

:::note Estado
Disponible por **MQTT en el broker de la plataforma**. HTTP y LwM2M no aceptan certificados de cliente, el SDK de C# todavía no puede presentar uno, y la renovación de un certificado por sí sola aún no está disponible. [Lo que no se admite](#not-supported) enumera todo lo que queda fuera.
:::

## El ancla de confianza es el límite de la identidad {#trust-anchor}

Un **ancla de confianza** es un certificado de CA que un tenant ha añadido para decir «los dispositivos con un certificado emitido por esta CA son míos». Es material público y se guarda tal cual. Un tenant puede tener 32.

- Una CA raíz o una CA intermedia puede ser un ancla. Ancla en la CA más estrecha que emita solo tus dispositivos. Si un fabricante opera una PKI para muchos clientes, confía en la subCA que emite tu flota, no en la raíz.
- Cada conexión se comprueba de nuevo contra el ancla. Nada de un certificado se guarda en caché como de confianza.
- Un ancla se puede deshabilitar. Eso retira la CA al instante, y se terminan las sesiones activas de todos los dispositivos que dependen de ella.

Como el ancla decide quién es un dispositivo del tenant, **las restricciones de nombre (name constraints) no se respetan**, y una CA que las lleve se rechaza al añadirla como ancla o cuando aparece en una cadena presentada. La identidad que usa DeviceChain es el nombre común del certificado, que las restricciones de nombre no cubren. Una restricción pensada para limitar lo que una CA puede emitir se ignoraría en silencio, así que la plataforma rechaza el certificado en lugar de dejarte creer que se aplica.

## Cómo debe ser un certificado {#certificate-rules}

La plataforma acepta un certificado de dispositivo solo cuando se cumple todo esto:

- Nombra la **autenticación de cliente** en su uso extendido de clave. Un certificado sin uso extendido de clave, o que permita cualquier uso, se rechaza: nunca declaró ser un certificado de cliente.
- No es una CA.
- Ni él ni ningún certificado por encima está firmado con SHA-1.
- Todas las claves de la cadena son RSA de 2.048 a 8.192 bits, ECDSA en P-256 o P-384, o Ed25519.
- Su cadena, desde la hoja hasta los intermedios que envía el dispositivo, llega hasta el ancla de confianza. El dispositivo envía como máximo cinco certificados, 16 KiB en total.
- Es válido ahora. Se acepta un certificado emitido hasta cinco minutos en el futuro, para perdonar a un dispositivo cuyo reloj va algo atrasado respecto al de la CA; su caducidad nunca se amplía.

## Dos formas de que un certificado se convierta en dispositivo {#two-ways}

| Forma | Qué haces | La identidad del dispositivo |
| --- | --- | --- |
| **Credencial fijada** | Creas el dispositivo y luego añades una credencial `X509_CERTIFICATE` con su certificado que nombra el ancla. | El dispositivo que creaste. Su token es el que elegiste. |
| **Registro en la primera conexión** | Creas un **perfil de certificado**: un perfil de aprovisionamiento que nombra un ancla. Los dispositivos cuyos certificados encadenan con el ancla se registran solos la primera vez que se conectan. | Un dispositivo creado para el certificado. Su id externo es el nombre común del certificado, y su token se deriva de él. |

Una **credencial fijada** se identifica por el SHA-256 del certificado, que calcula el servidor. La credencial registra el ancla con la que debe encadenar el certificado y la caducidad más temprana de toda la cadena. Un dispositivo puede tener hasta tres credenciales de certificado activas a la vez, de modo que se puede añadir un reemplazo antes de que caduque el anterior.

Un **perfil de certificado** no tiene un secreto de aprovisionamiento que proteger ni rotar, porque el certificado es la prueba. Usa los mismos controles que el [autorregistro](../guides/device-self-registration.md): la estrategia, un tope de dispositivos, una caducidad y la revocación de lo que registró. Un tenant puede tener ocho perfiles de certificado.

### El token derivado y el client id {#derived-token}

Las sesiones MQTT se identifican por el client id, y la plataforma exige que sea `{instanceId}:{tenant}:{deviceToken}` para que un dispositivo no pueda apoderarse de la sesión de otro. Un dispositivo que se registra en la primera conexión aún no tiene token, así que la plataforma necesita un token que pueda calcular solo a partir del certificado, antes de escribir nada. El token de un dispositivo registrado desde un certificado es:

```text
pv-{slug}-{hash}
```

- `slug` es el nombre común con cada carácter que no sea letra, dígito, `-` o `_` sustituido por `-`, y sin `-` ni `_` al principio ni al final.
- `hash` son los primeros 12 caracteres hexadecimales del SHA-256 del nombre común.
- El token completo tiene como máximo 128 caracteres; un `slug` largo se recorta para que quepa.

Así, el firmware de un dispositivo puede calcular su propio client id a partir de su propio certificado. La [guía](../guides/device-x509.md#client-id) tiene un ejemplo resuelto. Una conexión cuyo client id no coincide se rechaza antes de crear nada, de modo que una conexión rechazada nunca deja un dispositivo a medias.

Un dispositivo que creas de antemano y reservas para un perfil de certificado debe llevar este token. La consola y la API lo rellenan cuando dejas el token vacío, y rechazan uno distinto.

### Qué perfil registra un dispositivo {#narrowest-anchor}

Un certificado puede encadenar con las anclas de más de un perfil. Gana el perfil cuyo ancla está **más cerca del certificado**, de modo que un perfil anclado en un intermedio tiene prioridad sobre uno anclado en su raíz. Si dos perfiles utilizables empatan a la misma distancia, la conexión se rechaza en lugar de adivinar, y salta la alerta `DeviceX509JitMisconfigured`. Da a cada perfil su propio ancla, o ancla uno en una CA más estrecha.

El registro en la primera conexión **crea** dispositivos con la estrategia `ALLOW_NEW`. Con `ALLOW_NEW`, cualquier certificado que emita la CA del ancla, con un nombre común que ningún dispositivo haya usado, registra un dispositivo nuevo, hasta el `maxDevices` del perfil. Si eso es demasiado abierto, ancla en el intermedio más estrecho, fija un tope y deshabilita el ancla ante un compromiso. Adoptar un dispositivo que creaste y reservaste de antemano, y aceptar un certificado renovado para un dispositivo que ya tiene uno, todavía no están disponibles.

## Cuánto dura una sesión {#session-lifetime}

La sesión se concede hasta la `notAfter` más temprana entre el certificado del dispositivo, cada intermedio y el ancla de confianza (y la propia caducidad de la credencial, si la tiene). El broker cierra la sesión en ese momento. Un dispositivo que renueva su certificado se reconecta con el nuevo.

## Revocación {#revocation}

| Necesitas | Haz esto | Surte efecto |
| --- | --- | --- |
| Detener un dispositivo | Deshabilita o elimina su credencial `X509_CERTIFICATE`, o deshabilita el dispositivo | Al instante; se termina la sesión abierta |
| Detener una CA entera | Deshabilita el ancla de confianza | Al instante; se termina la sesión de todos los dispositivos que dependen de ella |
| Detener un dispositivo que se registró solo | `revokeProvisionedDevices` o `resetDeviceProvisioning`, como con cualquier [dispositivo autorregistrado](../guides/device-self-registration.md#compromise) | Al instante |
| Dejar que un certificado caduque | Emite certificados de corta duración | Exactamente al caducar el certificado |
| Detener un certificado robado que nunca se conectó | Crea los dispositivos de antemano y fija sus certificados, o deshabilita el ancla | No antes de que ocurra una de esas dos cosas |

**No hay CRL ni OCSP.** La plataforma no descarga ni carga listas de revocación y no pregunta a un respondedor si un certificado sigue siendo bueno. Un certificado que quieres detener se detiene deshabilitando su credencial o su ancla. Planifícalo así: emite certificados de corta duración desde tu CA y apóyate en la caducidad, con la deshabilitación de credenciales y anclas como herramientas inmediatas.

## Lo que no se admite {#not-supported}

- **Restricciones de nombre.** Una CA con restricciones de nombre se rechaza, como se explicó arriba.
- **Listas de revocación de certificados y OCSP.** Consulta [Revocación](#revocation).
- **Renovación por sí sola, y adoptar un dispositivo después de restablecer su aprovisionamiento.** Un dispositivo que tiene un certificado y luego presenta otro distinto se rechaza (`ALREADY_PROVISIONED`) cuando se conecta para registrarse. Hasta que llegue la renovación, añade el certificado nuevo como credencial fijada (un dispositivo puede tener tres) antes de que caduque el anterior.
- **El SDK de C#.** Todavía no puede presentar un certificado de cliente.
- **Ingesta HTTP y LwM2M.** Ninguna se autentica con un certificado de cliente. Los dispositivos HTTP usan un token o una credencial básica, y los de LwM2M usan claves precompartidas. Un certificado presentado por MQTT con un usuario distinto de `{tenant}:@x509` se ignora, y la conexión se juzga por su token o contraseña como antes.
- **Otros brokers.** Solo el broker propio de la plataforma autentica por certificado. Un dispositivo que se conecta a través de un broker externo del operador no está cubierto.
- **Certificados emitidos por la plataforma.** Tú traes tu propia CA y tus propios certificados.
