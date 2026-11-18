---
sidebar_position: 7
title: API de dispositivos de un vistazo
---

# API de dispositivos de un vistazo {#device-apis}

Todas las formas en que un dispositivo puede llegar a DeviceChain, cada una con un comando que puedes pegar en una terminal. Esta página es un mapa, no una especificación: cada fila enlaza a la página que explica el transporte, y la [Referencia del protocolo de dispositivos](./device-protocol.md) lista cada campo y cada rechazo. Lo que cada transporte puede y no puede hacer está en la [Matriz de capacidades por transporte](./transport-matrix.md).

Los ejemplos usan la instalación local en kind (tenant `acme`, instancia `devicechain`). Sustituye el host, el tenant y las credenciales por los tuyos.

| Transporte | Dirección | Se autentica con | Ir a |
| --- | --- | --- | --- |
| [MQTT](#mqtt) | telemetría de entrada, comandos de salida | id y secreto de credencial, por conexión | [Conectar un dispositivo](../guides/connecting-a-device.md#mqtt) |
| [HTTP](#http) | telemetría de entrada, recepción de comandos por consulta opcional | credencial en el cuerpo para eventos; token de acceso bearer para consultar comandos | [Conectar un dispositivo](../guides/connecting-a-device.md#http) |
| [LwM2M](#lwm2m) | telemetría de entrada, escrituras de salida | clave precompartida DTLS | [LwM2M](../concepts/lwm2m.md) |
| [Sparkplug B](#sparkplug) | telemetría de entrada, comandos de salida | la credencial del broker de la fuente | [Sparkplug B](../concepts/sparkplug.md) |
| [Certificado X.509](#x509) | como MQTT | certificado de cliente | [Dispositivos con certificado](../guides/device-x509.md) |
| [Autorregistro](#self-registration) | obtener un dispositivo y una credencial | clave y secreto de aprovisionamiento | [Autorregistro](../guides/device-self-registration.md) |

## MQTT {#mqtt}

Publica un [evento de dispositivo](./device-protocol.md#device-event) en el topic de eventos del dispositivo en el broker de la plataforma. El usuario es `{tenant}:{credentialId}` y el cuerpo del evento no lleva credencial.

```bash
mosquitto_pub --cafile instance-ca.crt -h <mqtt-host> -p 1883 \
  -i 'devicechain:acme:sensor-001' -u 'acme:<credentialId>' -P '<credentialSecret>' \
  -t 'devicechain/acme/devices/sensor-001/events' \
  -m '{"device":"sensor-001","eventType":"Measurement","payload":{"entries":[{"measurements":{"temperature":"21.5"}}]}}'
```

Los comandos llegan a un topic al que el dispositivo se suscribe:

```bash
mosquitto_sub --cafile instance-ca.crt -h <mqtt-host> -p 1883 \
  -i 'devicechain:acme:sensor-001' -u 'acme:<credentialId>' -P '<credentialSecret>' \
  -t 'devicechain/acme/device-commands/sensor-001'
```

## HTTP {#http}

Envía con `POST` el mismo cuerpo de evento al servicio `event-sources`. Por HTTP la credencial viaja en el cuerpo, y la respuesta es `202` cuando el evento se aceptó. Un `429`, o un `503` con `Retry-After`, significa que debes reenviarlo más tarde.

```bash
curl -i -X POST http://localhost:8081/devicechain/acme/events \
  -H 'Content-Type: application/json' \
  -d '{"device":"sensor-001","eventType":"Measurement","credentialType":"ACCESS_TOKEN","credentialId":"<token>","payload":{"entries":[{"measurements":{"temperature":"21.5"}}]}}'
```

Para la [recepción de comandos por consulta HTTP](../concepts/commands.md#http-command-pull), fija `commandChannel` del tipo de dispositivo en `HTTP_PULL`. El dispositivo consulta comandos, confirma su recepción y envía respuestas usando `Authorization: Bearer <access-token>` con su propio token de acceso. El canal predeterminado `PUBLISH` no admite consultas. Este canal HTTP no transporta configuración ni asignaciones de actualizaciones gestionadas. Mantén el puerto 8081 detrás de controles de red.

## LwM2M {#lwm2m}

Un cliente LwM2M se registra por CoAP sobre DTLS, **UDP 5684**, presentando una identidad de clave precompartida que la plataforma resuelve a un tenant y un dispositivo. Sirve cualquier cliente LwM2M con soporte PSK; por ejemplo, con el cliente de demostración de Leshan:

```bash
./leshan-client-demo -u coaps://<lwm2m-host>:5684 -n sensor-001 -i <pskIdentity> -p <pskKeyHex>
```

La plataforma observa entonces los objetos de sensor del dispositivo y almacena cada notificación como una medición. Consulta [LwM2M](../concepts/lwm2m.md) para la presencia, las escrituras y lo que no se admite.

## Sparkplug B {#sparkplug}

La plataforma es un host Sparkplug: se suscribe a tu propio broker y no acepta la conexión del nodo de borde. Apunta una [fuente](../concepts/sparkplug.md) configurada al broker y publica un nacimiento de nodo seguido de datos, como de costumbre. Los nombres de topic son los de Sparkplug; las cargas son Protobuf, así que las construye una biblioteca Sparkplug. Con un cliente MQTT simple puedes comprobar que el topic es alcanzable:

```bash
mosquitto_sub -h <your-broker> -p 1883 -u <user> -P <password> -t 'spBv1.0/plant-a/#' -v
```

Cada `{group}/{node}` pasa a ser el id externo de un dispositivo en el tenant de la fuente.

## Certificado X.509 {#x509}

Un dispositivo con un certificado emitido por una CA de confianza se conecta con el usuario `{tenant}:@x509` y sin contraseña.

```bash
mosquitto_pub --cafile instance-ca.crt --cert device.crt --key device.key \
  -h <mqtt-host> -p 1883 -i 'devicechain:acme:<deviceToken>' -u 'acme:@x509' \
  -t 'devicechain/acme/devices/<deviceToken>/events' \
  -m '{"device":"<deviceToken>","eventType":"Measurement","payload":{"entries":[{"measurements":{"temperature":"21.5"}}]}}'
```

## Autorregistro {#self-registration}

Un dispositivo con una clave y un secreto de aprovisionamiento se crea a sí mismo y crea su credencial. Genera y guarda la credencial antes de enviar, y luego regístrate:

```bash
curl -sS https://<instance-host>/api/device-management/provisioning/acme/register \
  -H 'Content-Type: application/json' \
  -d '{"provisionKey":"acme-sensors-2026","provisionSecret":"<secret>","externalId":"SN-000123","credentialSecret":"<credential you saved>"}'
```

La respuesta nombra el token del dispositivo y el id de cliente MQTT con el que conectarse. Repetir la misma petición es seguro y responde `REPLAYED`. Consulta el [contrato de reintentos](./device-protocol.md#self-registration-retry).
