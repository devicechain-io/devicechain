---
title: Autenticación por certificado
---

# Autenticación por certificado {#certificate-authentication}

Esta página es el lado del operador de los [certificados de cliente de dispositivo](../concepts/device-certificates.md): la compilación del broker que los hace posibles, los ajustes que acotan el registro en la primera conexión, qué vigilar y qué cambia una actualización en una instancia que ya está en marcha. Para conectar un dispositivo, consulta [Conectar con un certificado](../guides/device-x509.md) y, para la consola, [Aprovisionamiento y anclas de confianza](../user-guide/provisioning-and-trust-anchors.md).

## La compilación del broker {#broker}

El servidor NATS no solicita un certificado de cliente en su listener MQTT del modo que necesita DeviceChain: su archivo de configuración puede desactivar los certificados o exigir uno a todos los clientes. Por eso el broker de la plataforma es la compilación propia de DeviceChain de `nats-server`, `dc-nats-server`: el `v2.15.1` original sin modificar, más un ajuste, aplicado al arrancar y en cada recarga de configuración. El listener MQTT **solicita, pero no exige,** un certificado de cliente.

- **Los dispositivos con token y contraseña no se ven afectados.** Un cliente que no envía certificado se conecta como antes. Un certificado enviado con un usuario de token o de contraseña se ignora.
- **El callout de autenticación hace la comprobación.** El broker entrega al callout de device-management los certificados que presentó el cliente, y este verifica la cadena. El broker en sí no decide nada sobre la confianza.
- **Imagen.** `ghcr.io/devicechain-io/dc-nats-server`, firmada y verificable como las demás imágenes ([Verificar versiones](./verifying-releases.md)). `dcctl` la fija por el digest que subió la versión, de modo que una versión que no cambió el broker no lo reinicia. Añádela a las imágenes que replicas.
- **El broker se ejecuta como root,** igual que la imagen original, para poder abrir un almacén de JetStream que escribió un servidor de la imagen original.
- **Una sola imagen de broker admitida.** La imagen original es solo una vía de reversión.

### Vigilar el broker {#watching}

El servidor comprueba, haciendo un handshake con su propio puerto MQTT al arrancar, tras cada recarga y cada minuto, que el listener sigue solicitando certificados. Exporta el resultado en el puerto `7778` (un endpoint `dc-metrics` en el PodMonitor del chart):

| Métrica | Significado |
| --- | --- |
| `dc_broker_mqtt_client_auth_request` | `1` mientras el listener MQTT solicita certificados de cliente, `0` cuando no |
| `dc_broker_mqtt_tls` | `1` mientras el listener MQTT sirve TLS |
| `dc_broker_info{library_version,build}` | qué binario se está ejecutando |

`BrokerMqttClientAuthLost` salta cuando un listener TLS ha dejado de solicitar certificados, o cuando un pod del broker no exporta `dc_broker_info`, que es el aspecto de la imagen original. Está en el [runbook de alertas](./observability.md). Mientras salta, los dispositivos que se autentican con certificado se rechazan (`no_cert`); los de token y contraseña no.

### Volver al broker original {#rollback}

Fija las variables de infraestructura `server_image_repository = "nats"` y `server_image_tag = "2.15.1-alpine"`. Entonces se rechaza a todos los dispositivos con certificado, porque el broker original nunca pide un certificado, y `BrokerMqttClientAuthLost` explica por qué. Nada más se ve afectado.

## Ajustes {#settings}

El registro en la primera conexión tiene dos ajustes fijos, que se leen al arrancar device-management, bajo el `config` del área:

```yaml
functionalAreas:
  device-management:
    config:
      callout:
        jitMaxInFlight: 16       # default 16, 1 to 128
        jitSourceTrusted: false  # default false
```

| Clave | Significado |
| --- | --- |
| `callout.jitMaxInFlight` | Cuántos registros en la primera conexión atiende a la vez una réplica. El siguiente se rechaza (`jit_busy`) y el dispositivo espera y reintenta. Estos puestos salen del límite propio del callout, así que una oleada de primeras conexiones nunca quita los puestos que necesitan los dispositivos que se reconectan, y un certificado fijado, un token o una contraseña nunca esperan por ellos. |
| `callout.jitSourceTrusted` | Añade un límite por dirección de cliente, dimensionado por `provisioning.sourceBurst` y `provisioning.sourceEvery`. Actívalo **solo** cuando el Service de MQTT conserve las direcciones de cliente (`externalTrafficPolicy: Local`, o un NodePort de kind). Tras un NAT de origen todos los dispositivos comparten una dirección y una cuota, y los reintentos de un dispositivo rechazarían a toda la flota. Desactivado, los rechazos sostenidos generan alertas en lugar de limitarse por dirección. |

El límite por tenant no es un ajuste de aquí: es el techo de aprovisionamiento del tenant, el mismo que cobra el [autorregistro](../guides/device-self-registration.md), que se gestiona con los demás límites de gobernanza del tenant. Los registros contra un perfil se turnan en ese perfil, a unos 100 a 200 por segundo por perfil entre todas las réplicas, así que una oleada de primeras conexiones de 10.000 dispositivos se registra en uno o dos minutos de reintentos de los dispositivos. La cuota del tenant se cobra por cada intento cuya cadena se verificó, incluso uno rechazado después, así que un dispositivo que reintenta con un certificado rotado la gasta.

Una clave desconocida bajo `callout`, o un valor fuera de rango, detiene el servicio al arrancar.

## Límites {#limits}

| Límite | Valor |
| --- | --- |
| Anclas de confianza por tenant | 32 |
| Perfiles de certificado por tenant | 8 |
| Credenciales de certificado activas por dispositivo | 3 |
| Certificados que presenta un dispositivo | 5, como máximo 16 KiB |
| Archivo del ancla de confianza | un certificado de CA |

## Qué vigilar {#monitoring}

El contador `callout_x509_total{result}` cuenta cada conexión con certificado por resultado, y todos los resultados existen a cero desde el arranque. Los resultados incluyen `ok`, `no_cert`, `chain_invalid`, `anchor_disabled`, `revoked`, `client_id_mismatch`, `unknown_pin`, `x509_cert_ignored`, `jit_created`, `jit_replayed`, `jit_rate_limited`, `jit_busy`, `jit_ambiguous` y un `jit_<code>` por cada rechazo de registro. `callout_jit_in_flight` son los registros en curso ahora, y `device_trust_anchors_expiring{window="30d"}` es un recuento de toda la instancia de anclas habilitadas que caducan en 30 días.

Las [alertas](./observability.md#certificate-alerts) son `DeviceX509ChainFailuresSustained`, `DeviceX509JitRefusedSustained`, `DeviceX509JitMisconfigured`, `DeviceTrustAnchorsExpiringSoon` y, para el broker, `BrokerMqttClientAuthLost`.

Los cambios de anclas y credenciales se anotan en el registro de auditoría. Los resultados de conexión se cuentan, no se anotan.

Revocar un dispositivo o deshabilitar un ancla termina las sesiones abiertas de los dispositivos afectados. Si la plataforma no puede confirmar que una sesión terminó, el cambio se conserva y la plataforma sigue reintentando hasta que la sesión desaparece; repetir el cambio vuelve a preguntar.

## Actualizar una instancia que ya está en marcha {#cutover}

En una instancia que ya está en marcha, la autenticación por certificado se activa añadiendo un ancla de confianza, no actualizando.

- **Los clientes con token y contraseña no se ven afectados.** El listener MQTT del broker ahora pide un certificado a cada cliente; un cliente que no tiene ninguno responde con uno vacío y se conecta como antes. Lo mismo vale para un cliente que envía un certificado con un token o una contraseña: se ignora. Antes de actualizar una flota, prueba con un dispositivo de cada pila TLS que use.
- **La imagen del broker cambia si aún no lo ha hecho.** Una instancia que viene de una versión que todavía ejecutaba el broker original reinicia su broker una vez, y un reinicio del broker abre una breve ventana en la que se pueden perder mensajes MQTT ya confirmados salvo que el MQTT fail-closed esté activado; consulta [el transporte MQTT](../reference/transport-matrix.md#mqtt--the-platform-broker). Una instancia que ya está en `dc-nats-server` no se reinicia cuando el digest no ha cambiado.
- **Nada se registra ni se acepta automáticamente.** Después de actualizar no existe ningún ancla de confianza, así que ningún certificado autentica nada hasta que añadas uno.
- **Una credencial de certificado anterior a la compatibilidad con certificados no autentica.** El tipo se rechazaba en versiones anteriores, y una fila existente no tiene ancla de confianza. Créala de nuevo como credencial fijada que nombre un ancla.
- **El esquema gana** una tabla de anclas de confianza y columnas para el ancla en las credenciales y en los perfiles de aprovisionamiento. La actualización las aplica; no se reescribe ningún dato.

Todavía no están disponibles, y no forman parte de la actualización: la renovación del certificado de un dispositivo registrado, adoptar un dispositivo tras restablecer su aprovisionamiento, un certificado de cliente en el SDK de C# y la autenticación por certificado por HTTP o LwM2M. La [página de conceptos](../concepts/device-certificates.md#not-supported) tiene la lista completa, incluido que las CA con restricciones de nombre se rechazan y que no hay CRL ni OCSP.
