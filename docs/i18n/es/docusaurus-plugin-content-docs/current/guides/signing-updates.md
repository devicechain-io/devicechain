---
sidebar_position: 12
title: Firmar actualizaciones
---

# Firmar actualizaciones

Una comprobación del digest demuestra que una imagen es la que se declaró, pero no quién la creó. Esta guía explica cómo añadir esa prueba: registras las claves públicas con las que firma tu organización, firmas el digest de cada artefacto y dejas que la plataforma entregue la firma correspondiente al dispositivo, que toma la decisión final.

:::note Estado
La firma se entrega con el registro de firmantes del servicio de actualizaciones. Hasta que tu instancia ejecute una versión que lo incluya, los artefactos permanecen `UNVERIFIED` y las asignaciones no llevan firma. Para los conceptos, consulta [Actualizaciones over-the-air](../concepts/over-the-air-updates.md#verified); para los mensajes que recibe un dispositivo, consulta [Conexión de un dispositivo](./connecting-a-device.md#over-the-air-updates).
:::

## Cómo funciona la confianza

- Un **firmante** es una clave pública en la que confía el tenant. La plataforma guarda solo la mitad pública y rechaza una clave privada.
- Una **firma** se hace sobre el digest SHA-256 del artefacto y se adjunta a él. La plataforma la verifica contra el digest que guardó al subirlo.
- El **estado de confianza** de un artefacto se deriva cada vez que se lee: `SIGNED` mientras al menos un firmante activo tenga una firma verificada sobre él, y `UNVERIFIED` en caso contrario. Revocar un firmante cambia, por tanto, todos los artefactos que avalaba, sin escribir en ellos.
- Asignar un artefacto `UNVERIFIED` se rechaza salvo que la instancia permita artefactos sin verificar (`updates.allowUnverifiedArtifacts`).
- El **dispositivo** verifica la firma con claves que ya posee. La plataforma retransmite la firma; nunca decide por el dispositivo.

Intervienen dos autorizaciones, separadas a propósito: `artifact:trust` decide qué firma cuenta (registrar y revocar firmantes), y `artifact:write` publica artefactos y adjunta firmas.

## 1. Generar una clave

Usa ECDSA sobre P-256 o Ed25519. No se acepta ningún otro tipo de clave ni curva. Mantén la clave privada fuera de la plataforma y de los servidores de compilación que no la necesiten.

```bash
# ECDSA P-256
openssl ecparam -name prime256v1 -genkey -noout | openssl pkcs8 -topk8 -nocrypt -out signer.pem

# o Ed25519
openssl genpkey -algorithm ed25519 -out signer.pem

# la mitad pública, como bloque PEM SPKI (esto es lo que se registra)
openssl pkey -in signer.pem -pubout -out signer.pub.pem
```

## 2. Calcular el identificador de clave

La identidad de una clave, el `keyId` que busca un dispositivo, es el SHA-256 en hexadecimal minúsculo de su clave pública como `SubjectPublicKeyInfo` DER:

```bash
openssl pkey -in signer.pem -pubout -outform DER | openssl dgst -sha256 -r | cut -d' ' -f1
```

La plataforma calcula el mismo valor al registrar la clave y rechaza un segundo registro de la misma clave. El token del firmante es `signer-` seguido de los primeros 16 caracteres hexadecimales del identificador de clave.

## 3. Registrar el firmante

Requiere `artifact:trust`. Pasa solo el PEM público.

```graphql
mutation {
  registerArtifactSigner(
    name: "Release signing key 2026"
    publicKeyPem: "-----BEGIN PUBLIC KEY-----\nMFkw…\n-----END PUBLIC KEY-----\n"
  ) {
    token
    keyId
    algorithm
    state
  }
}
```

Se rechaza una clave privada, cualquier otro tipo de clave o curva, o una clave ya registrada. `artifactSigners` lista los firmantes del tenant con su estado, y cada uno informa `soleSignedArtifactCount`, el número de artefactos que volverían a `UNVERIFIED` si se revocara.

## 4. Firmar el digest de un artefacto

Ambos algoritmos firman el **digest SHA-256 en bruto de 32 bytes** del artefacto. Firma los bytes que subiste; el resultado debe verificarse contra el `digest` con el que se declaró el artefacto.

**ECDSA P-256.** `openssl dgst -sha256 -sign` calcula el hash del archivo y firma el digest, y produce la firma ASN.1 DER que esperan la plataforma y los dispositivos:

```bash
openssl dgst -sha256 -sign signer.pem -binary firmware.bin | base64 | tr -d '\n'
```

Según su documentación, `cosign sign-blob --key` produce una firma de la misma forma con claves ECDSA, por lo que su salida en base64 también debería aceptarse. Ese comportamiento proviene de la documentación de cosign y no se probó para esta guía; compruébalo con un artefacto de prueba antes de depender de él.

**Ed25519.** Ed25519 firma un mensaje directamente, así que el mensaje debe ser los 32 bytes del digest, no el archivo:

```bash
openssl dgst -sha256 -binary firmware.bin > digest
openssl pkeyutl -sign -rawin -inkey signer.pem -in digest | base64 | tr -d '\n'
```

:::warning No firmes el archivo con Ed25519
`openssl pkeyutl -sign -rawin -in firmware.bin` (y las herramientas que firman blobs directamente) firma el propio archivo. Esa firma es criptográficamente válida y aun así incorrecta: la plataforma y el dispositivo verifican contra el digest, por lo que se rechazará al adjuntarla o el dispositivo la rechazará durante la instalación. Firma siempre el archivo `digest` como se muestra.
:::

Estas recetas se comprobaron con OpenSSL 3.2.1 frente a un verificador independiente que calcula el SHA-256 del archivo y llama a la verificación ECDSA y Ed25519 de la biblioteca estándar de Go: ambas firmas anteriores se verifican, y una firma Ed25519 hecha sobre el archivo en lugar del digest no.

## 5. Adjuntar la firma

Requiere `artifact:write`. La plataforma verifica la firma contra el digest almacenado del artefacto antes de conservarla.

```graphql
mutation {
  addArtifactSignature(
    artifactToken: "fw-1-4-2"
    signerToken: "signer-b1b9eef5a4de45e2"
    signature: "MEQCIHGf…"
  ) {
    token
    trustStatus
  }
}
```

Se rechaza una firma que no se verifica, y también la de un firmante revocado. Volver a firmar con el mismo firmante no cambia nada. Un artefacto puede llevar firmas de varios firmantes. Al asignarlo, la plataforma fija la firma **más antigua** de un firmante activo, y al dispositivo se le envía esa firma con su `keyId` y algoritmo (`ECDSA_P256_SHA256` o `ED25519`).

Un artefacto lista las firmas registradas en su campo `signatures`, de la más antigua a la más reciente. Cada entrada indica el `signer`, `createdAt`, `createdBy` y `countsTowardTrust`, que es verdadero mientras el firmante está activo y falso una vez revocado. Las firmas de firmantes revocados siguen en la lista. Los bytes de la firma nunca se devuelven. La página del artefacto en la consola muestra la misma lista en **Firmas y confianza**.

## 6. Poner la clave en los dispositivos

La plataforma no gestiona la confianza en el dispositivo. Todo dispositivo que deba aceptar tus actualizaciones debe tener la clave pública (o al menos su `keyId` y la clave) antes de recibir una asignación firmada. Falla la actualización con `UNKNOWN_SIGNER` para un `keyId` que no tiene y con `SIGNATURE_INVALID` para una firma que no se verifica. Aprovisiona las claves en la imagen de fábrica o mediante una actualización de configuración que ya hayas verificado.

Un dispositivo debe tener la clave de **todos los firmantes cuya firma pudiera fijarse** en una asignación que pueda recibir, no solo tu clave más reciente.

## Rotar claves

1. Genera y registra el nuevo firmante.
2. Entrega la nueva clave pública a todos los dispositivos y espera a que tu flota la tenga. Hazlo **antes** de firmar nada con ella.
3. Firma los artefactos nuevos con la clave nueva. Si un artefacto lleva ambas firmas, se fija la activa más antigua, de modo que la clave antigua se sigue usando hasta que se revoca.
4. Revoca el firmante antiguo cuando ningún dispositivo lo necesite.

No revoques la clave antigua antes de que la nueva esté en los dispositivos. Los dispositivos que aún no la hayan recibido recibirían firmas que no pueden comprobar.

## Revocar un firmante

Requiere `artifact:trust`.

```graphql
mutation {
  revokeArtifactSigner(token: "signer-b1b9eef5a4de45e2") { token state revokedAt }
}
```

La revocación es terminal e idempotente. Las firmas del firmante permanecen registradas y dejan de contar:

- Los artefactos que solo él avalaba pasan a `UNVERIFIED` de inmediato, y se rechazan nuevas asignaciones de ellos salvo que la instancia permita artefactos sin verificar.
- Una asignación ya fijada a esa firma la conserva, de modo que una actualización en curso se entrega tal como se asignó.
- Si la plataforma no puede leer la firma fijada de una asignación, no envía nada en lugar de enviar la actualización sin firma.
- Con el indicador de la instancia desactivado, no se envía una asignación sin firma fijada (una hecha mientras se permitían artefactos sin verificar).

Comprueba antes `soleSignedArtifactCount` para ver qué afectará una revocación.

## Qué no cubre una firma {#what-a-signature-does-not-cover}

Una firma vincula **solo el digest**. No vincula la versión, el componente ni los perfiles compatibles del artefacto. Cualquiera con `artifact:write` puede declarar de nuevo una imagen firmada antes bajo una versión nueva, y la firma antigua se verificará. Si necesitas rechazar degradaciones o instalaciones en el componente equivocado, debe imponerlo el propio dispositivo, y los permisos de publicación deben protegerse tanto como los de firma. [Construir un dispositivo actualizable](./building-an-updatable-device.md#refusing-downgrades) muestra cómo lo hace un dispositivo de referencia.

## Relacionado

- **[Firmantes](../concepts/over-the-air-updates.md#signers)**: cómo una firma hace que un artefacto sea de confianza y qué cambia al revocar un firmante.
- **[Actualizaciones over-the-air](../concepts/over-the-air-updates.md#assigning)**: asignar una actualización y cómo progresa un intento.
- **[Conexión de un dispositivo](./connecting-a-device.md#over-the-air-updates)**: el mensaje de asignación y la comprobación en el dispositivo.
- **[Construir un dispositivo actualizable](./building-an-updatable-device.md)**: un dispositivo de referencia que verifica, instala y revierte.
