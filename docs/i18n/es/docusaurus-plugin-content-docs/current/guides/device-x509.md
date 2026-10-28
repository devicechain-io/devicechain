---
sidebar_position: 3.7
title: Conectar con un certificado
---

# Conectar con un certificado {#connecting-with-a-certificate}

Un dispositivo puede autenticarse en el broker de la plataforma con un certificado de cliente X.509 en lugar de un token o una contraseña. Esta guía te lleva de una autoridad certificadora (CA) a un dispositivo que publica su primer evento, y muestra las dos formas en que un certificado puede convertirse en dispositivo: una **credencial fijada** que creas para un dispositivo que ya existe, y un **perfil de certificado** que registra dispositivos la primera vez que se conectan.

Cómo decide la plataforma confiar en un certificado, y dónde se detiene esa confianza, está en [Identidad por certificado](../concepts/device-certificates.md). Lee sus [límites](../concepts/device-certificates.md#not-supported) antes de planificar una flota con esto: no hay CRL ni OCSP, las CA con restricciones de nombre se rechazan, y la renovación y el SDK de C# todavía no están disponibles.

:::note Estado
Disponible por MQTT en el broker de la plataforma. HTTP y LwM2M no aceptan certificados de cliente.
:::

## Qué necesitas {#what-you-need}

- Una CA cuyos certificados vayas a aceptar, y un certificado para cada dispositivo. Esta guía crea una CA desechable con `openssl` para que puedas probar todo el recorrido; en producción usa tu propia PKI.
- Un tenant y permiso para gestionar dispositivos (`device:write`).
- El certificado de la CA de la instancia, para que el dispositivo pueda verificar el broker, como en [Conectar un dispositivo](./connecting-a-device.md#publishing-an-event).

## Crear una CA de prueba y un certificado de dispositivo {#mint}

El certificado del dispositivo debe llevar el uso extendido de clave de **autenticación de cliente** y no debe ser una CA. Su nombre común (CN) pasa a ser el id externo del dispositivo, así que usa el número de serie u otro id con el que quieras conocerlo.

```bash
# A CA: a P-256 key and a self-signed certificate that may sign certificates.
openssl ecparam -name prime256v1 -genkey -noout -out ca.key
openssl req -x509 -new -key ca.key -sha256 -days 3650 -subj "/CN=Acme Device CA" \
  -addext "basicConstraints=critical,CA:TRUE" \
  -addext "keyUsage=critical,keyCertSign,cRLSign" \
  -out ca.crt

# A device: its own key, and a certificate the CA signs for it. CN = the device's external id.
openssl ecparam -name prime256v1 -genkey -noout -out device.key
openssl req -new -key device.key -subj "/CN=SN-000123" -out device.csr
cat > device.ext <<'EOF'
basicConstraints=critical,CA:FALSE
keyUsage=critical,digitalSignature
extendedKeyUsage=clientAuth
EOF
openssl x509 -req -in device.csr -CA ca.crt -CAkey ca.key -CAcreateserial \
  -sha256 -days 90 -extfile device.ext -out device.crt

# Check what you made.
openssl x509 -in device.crt -noout -subject -ext extendedKeyUsage -enddate
openssl verify -CAfile ca.crt -purpose sslclient device.crt
```

El último comando debe imprimir `device.crt: OK`. Mantén en privado `ca.key` y `device.key`. A la plataforma solo van `ca.crt` y `device.crt`.

La plataforma acepta claves RSA de 2.048 a 8.192 bits, ECDSA en P-256 o P-384 y Ed25519, y rechaza firmas SHA-1 en cualquier punto de la cadena. Una vigencia de 90 días es deliberada: la plataforma no tiene lista de revocación, así que una vigencia corta es la forma de que un certificado no deseado desaparezca solo.

## Añadir la CA como ancla de confianza {#trust-anchor}

Un ancla de confianza le dice al tenant en qué CA creer. En la consola, abre **Anclas de confianza**, elige añadir una y pega o sube `ca.crt`. La consola rechaza un archivo que contenga una clave privada. Las páginas de la consola se describen en [Aprovisionamiento y anclas de confianza](../user-guide/provisioning-and-trust-anchors.md). Con GraphQL:

```graphql
mutation {
  createDeviceTrustAnchor(request: {
    token: "acme-device-ca"
    name: "Acme device CA"
    certificatePem: "-----BEGIN CERTIFICATE-----\n...\n-----END CERTIFICATE-----\n"
  }) { token fingerprintSha256 notAfter enabled }
}
```

El ancla debe ser exactamente un certificado de CA, sin caducar, con una clave admitida y sin restricciones de nombre. Si los certificados de tus dispositivos los emite una CA intermedia, ancla en la intermedia y no en la raíz, para que el tenant confíe solo en la CA que emite tu flota.

## Elegir cómo aparecen los dispositivos {#choose}

Elige una para cada flota.

### Opción A: fijar un certificado a un dispositivo que creaste {#pinned}

Úsala cuando creas los dispositivos tú y conoces el certificado de cada uno.

1. Crea el dispositivo (consulta [Importar dispositivos](./importing-devices.md) para muchos a la vez). Su token lo eliges tú.
2. Añade una credencial `X509_CERTIFICATE`. En la consola, abre la pestaña **Credenciales** del dispositivo, elige el tipo certificado, pega `device.crt` y elige el ancla. Con GraphQL:

   ```graphql
   mutation {
     createDeviceCredential(request: {
       token: "sn-000123-cert"
       deviceToken: "sensor-001"
       credentialType: "X509_CERTIFICATE"
       enabled: true
       certificatePem: "-----BEGIN CERTIFICATE-----\n...\n-----END CERTIFICATE-----\n"
       trustAnchorToken: "acme-device-ca"
     }) { token credentialId expiresAt certificate { subjectCn trustAnchor { token } } }
   }
   ```

3. No envíes un `credentialId`; se rechaza para este tipo. El servidor lo calcula como el SHA-256 del certificado y lo devuelve. Es público, y es la huella fijada.

La plataforma verifica la cadena al crear la credencial y rechaza una que no podría autenticar. Un dispositivo puede tener hasta tres credenciales de certificado activas, así que puedes añadir el siguiente certificado antes de que caduque el anterior. La caducidad de la credencial es la más temprana de toda la cadena.

### Opción B: registrar dispositivos en su primera conexión {#registration}

Úsala para una flota que sale de una línea de producción con los certificados ya instalados. No hace falta que exista ningún dispositivo de antemano.

1. Crea un **perfil de certificado**. En la consola, abre **Perfiles de aprovisionamiento**, elige el modo **Certificado** y elige el ancla. Con GraphQL, crea un perfil de aprovisionamiento que nombre el ancla:

   ```graphql
   mutation {
     createProvisioningProfile(request: {
       token: "acme-sensors-certs"
       provisionKey: "acme-sensors-certs"
       strategy: "ALLOW_NEW"
       deviceTypeToken: "acme-sensor"
       enabled: true
       maxDevices: 5000
       trustAnchorToken: "acme-device-ca"
     }) { profile { token trustAnchor { token } } issuedSecret }
   }
   ```

   `issuedSecret` vuelve nulo: un perfil de certificado no tiene secreto que grabar, proteger ni rotar.
2. Fija `maxDevices`. Con `ALLOW_NEW`, cualquier certificado que emita la CA del ancla con un nombre común que ningún dispositivo haya usado registra un dispositivo nuevo, así que el tope y un ancla estrecha son lo que acota el daño de un error.
3. Pon en marcha la flota. La primera vez que un dispositivo se conecta, la plataforma lo crea con el nombre común del certificado como id externo, le fija el certificado y lo deja entrar. Las conexiones posteriores usan la huella fijada.

El token del dispositivo se deriva de su nombre común, lo que importa para el client id de abajo. El registro en la primera conexión solo crea dispositivos nuevos. Un dispositivo que creaste y reservaste para el perfil todavía no puede registrarse por certificado, ni tampoco uno cuyo aprovisionamiento se restableció; dales una credencial fijada (opción A). Un dispositivo que tiene un certificado y luego presenta otro distinto se rechaza hasta que haya renovación, así que añade antes el certificado nuevo como credencial fijada.

## Conectar {#connect}

El dispositivo se conecta al mismo endpoint MQTT que cualquier otro, por TLS. Lo que cambia:

| Ajuste | Valor |
| --- | --- |
| Certificado de cliente | `device.crt` (seguido de los certificados de CA intermedios) y `device.key` |
| Usuario | `{tenant}:@x509` |
| Contraseña | ninguna. Una contraseña enviada con este usuario se rechaza |
| Client id | `{instanceId}:{tenant}:{deviceToken}`, como en todos los dispositivos |

La `@` hace imposible confundir el usuario con un id de credencial. El tenant sale del usuario, nunca del certificado.

```bash
mosquitto_pub \
  --cafile instance-ca.crt --cert device.crt --key device.key \
  -h <mqtt-host> -p 1883 \
  -i 'devicechain:acme:pv-SN-000123-06e08144e3e8' \
  -u 'acme:@x509' \
  -t 'devicechain/acme/devices/pv-SN-000123-06e08144e3e8/events' \
  -m '{"device":"pv-SN-000123-06e08144e3e8","eventType":"Measurement","payload":{"entries":[{"measurements":{"temperature":"21.5"}}]}}'
```

El cuerpo del evento y el topic son los mismos que en cualquier otro dispositivo; consulta [Conectar un dispositivo](./connecting-a-device.md). Un dispositivo cuyo certificado emitió una CA intermedia debe enviar esa intermedia después de su propio certificado, porque la plataforma construye la cadena con lo que presenta el dispositivo hasta el ancla. Envía la hoja y como máximo una intermedia: la plataforma acepta como máximo cinco certificados y 16 KiB, y algunas pilas TLS (mbedTLS con su búfer de salida de 16 KiB por defecto) no pueden enviar una cadena más larga.

### El client id {#client-id}

Un dispositivo fijado (opción A) usa el token que le diste, como siempre. Un dispositivo que se registra en la primera conexión (opción B) usa el token que la plataforma deriva de su nombre común, de modo que el dispositivo puede calcularlo a partir de su propio certificado:

```text
pv-{slug}-{hash}
```

- `slug`: el nombre común con cada carácter que no sea letra, dígito, `-` o `_` sustituido por `-`, y sin `-` ni `_` al principio ni al final.
- `hash`: los primeros 12 caracteres hexadecimales del SHA-256 del nombre común.
- El token completo tiene como máximo 128 caracteres; un `slug` largo se recorta para que quepa.

Con el shell, para el certificado de arriba:

```bash
CN=SN-000123
slug=$(printf '%s' "$CN" | sed -E 's/[^A-Za-z0-9_-]/-/g; s/^[-_]+//; s/[-_]+$//')
hash=$(printf '%s' "$CN" | openssl dgst -sha256 -hex | sed 's/^.*= //' | cut -c1-12)
echo "devicechain:acme:pv-$slug-$hash"     # devicechain:acme:pv-SN-000123-06e08144e3e8
```

Una primera conexión cuyo client id no coincide se rechaza antes de crear nada, de modo que un error no puede dejar un dispositivo a medio registrar. Un nombre común debe tener de 1 a 256 caracteres, no estar en blanco y no contener el carácter NUL.

Si creas un dispositivo de antemano para un perfil de certificado, deja su token vacío y dale el id externo: la consola y la API rellenan el token derivado y rechazan uno distinto.

## Cuando se rechaza una conexión {#refused}

Como en todos los rechazos de dispositivos, el dispositivo solo ve el código de retorno CONNACK 5. El motivo está del lado del operador, como la etiqueta `result` del contador `callout_x509_total`. Comprueba en este orden:

1. **`no_cert`.** El cliente no envió certificado. Revisa `--cert` y que la clave corresponda al certificado.
2. **`chain_invalid`.** La cadena no llega al ancla, un certificado ha caducado, al uso extendido de clave le falta la autenticación de cliente, una firma usa SHA-1 o una clave está fuera del conjunto permitido. Reprodúcelo con `openssl verify -CAfile ca.crt -purpose sslclient device.crt` y comprueba que el dispositivo envía sus intermedios.
3. **`anchor_disabled`, `revoked`.** El ancla o la credencial se deshabilitó a propósito.
4. **`client_id_mismatch`.** El client id no es `{instanceId}:{tenant}:{deviceToken}` de este dispositivo o, en una primera conexión, el derivado.
5. **`unknown_pin`.** El certificado no está fijado y no encadena con ningún perfil de certificado utilizable.
6. **`jit_*`.** Se rechazó un registro en la primera conexión: `jit_rate_limited` y `jit_busy` significan que el dispositivo debe esperar y reintentar, `jit_ambiguous` significa que dos perfiles empatan, y el resto lleva el código de error del registro.

[Observabilidad](../deployment/observability.md#certificate-alerts) enumera las alertas y qué hacer con cada una. Un certificado enviado con un usuario de token o de contraseña se ignora (se cuenta como `x509_cert_ignored`) y la conexión se juzga por el token o la contraseña.

## Reintentos y vigencias {#lifetimes}

- **Reintenta con espera creciente.** Un dispositivo rechazado con `jit_rate_limited` o `jit_busy` no está equivocado; MQTT no tiene forma de decir «inténtalo más tarde». Reintenta con espera exponencial y jitter completo, para que una tirada de fábrica no machaque el broker.
- **La sesión termina en la primera caducidad.** La plataforma cierra la sesión cuando caduca el primer certificado de la cadena, sin margen. Renueva antes y reconéctate con el certificado nuevo. Hasta que haya renovación, un dispositivo necesita el certificado nuevo fijado como segunda credencial (opción A) antes de que caduque el anterior.
- **Un margen de 5 minutos** cubre un certificado emitido hace un momento para un dispositivo cuyo reloj va algo atrasado.

## Pruébalo sin firmware {#simulator}

El simulador representa dispositivos con certificado: `dc-simulator --x509` crea una CA desechable y certificados (ECDSA P-256, autenticación de cliente, CN = id externo), los registra mediante la API con `--x509-register` y conecta cada uno. `--x509-jit N` crea certificados para dispositivos que nunca registra, para probar un perfil de certificado. Consulta el README del simulador para los indicadores.

## Lo que esto no hace {#limits}

- **Sin CRL ni OCSP.** Para detener un certificado, deshabilita su credencial (o el dispositivo, o el ancla) y emite certificados de corta duración.
- **Una CA con restricciones de nombre se rechaza.** El ancla de confianza es el límite de la identidad.
- **Sin renovación de un dispositivo registrado presentando un certificado nuevo, ni adopción de un dispositivo restablecido.** Todavía no está disponible.
- **Sin certificado de cliente en el SDK de C#.** Todavía no está disponible.
- **Sin certificados por HTTP ni LwM2M.**

La [página de conceptos](../concepts/device-certificates.md#not-supported) explica por qué. Para el lado del broker, incluido lo que cambia al actualizar una instancia existente, consulta [Autenticación por certificado](../deployment/device-certificates.md).
