---
sidebar_position: 11
title: Verificar las versiones
---

# Verificar las versiones {#verifying-releases}

Cada imagen de contenedor y el chart de Helm que publica una versión de DeviceChain está **firmado**, y lleva dos declaraciones firmadas sobre cómo se construyó. Puede comprobarlo todo antes de ejecutar nada, sin cuenta y sin ninguna clave que obtener.

| Qué | Dónde está | Qué le dice |
| --- | --- | --- |
| **Firma** | Junto a la imagen o el chart en el registro | La publicó el flujo de trabajo de versiones de DeviceChain, para esa etiqueta exacta. |
| **SBOM** (SPDX) | Junto a cada imagen, como atestación firmada | Los paquetes que contiene la imagen. |
| **Procedencia de la compilación** (SLSA) | Junto a cada imagen y al chart, y en el almacén de atestaciones de GitHub | Qué commit, flujo de trabajo y ejecución la construyó. |

La firma es **sin clave**: no hay ninguna clave de larga duración que pueda filtrarse o rotarse. Cada firma queda ligada a la identidad de la ejecución del flujo de trabajo que la produjo, se registra en un registro de transparencia público y se verifica contra esa identidad. Una firma de cualquier otro flujo de trabajo, repositorio o rama no se verifica como una versión.

:::note Qué versiones
La firma se aplica a las versiones cuyas notas la anuncian, a más tardar `v1.0.0`. Las imágenes anteriores no llevan firma, y verificar una falla con «no signatures found». Esa es la respuesta correcta, no un fallo de su configuración.
:::

## Qué está firmado {#what}

- cada imagen de servicio: `ghcr.io/devicechain-io/<servicio>` (por ejemplo `device-management`)
- la imagen del operador de Kubernetes: `ghcr.io/devicechain-io/operator`
- la imagen del agente de borde: `ghcr.io/devicechain-io/dc-edge-agent`
- la imagen de la consola web: `ghcr.io/devicechain-io/frontend`
- el chart de Helm: `oci://ghcr.io/devicechain-io/charts/devicechain`

Las imágenes llevan la etiqueta de la versión (`v1.0.0`). El chart lleva la versión **sin** la `v` (`1.0.0`). Los archivos de `dcctl` y del agente de borde adjuntos a la versión de GitHub tienen su propia procedencia; consulte [Versiones y actualizaciones](./releases-and-upgrades.md).

## Verificarlo todo de una vez {#script}

El repositorio incluye un script que comprueba todas las imágenes y el chart, y después demuestra que su propia comprobación puede fallar. Necesita [`cosign`](https://docs.sigstore.dev/cosign/system_config/installation/) 3 o posterior, `jq` y la [CLI de GitHub](https://cli.github.com/) con la sesión iniciada (`gh auth login`) para la procedencia.

```bash
curl -fsSLO https://raw.githubusercontent.com/devicechain-io/devicechain/v1.0.0/hack/verify-release-signatures.sh
chmod +x verify-release-signatures.sh
./verify-release-signatures.sh v1.0.0
```

Obtenga el script de la **misma etiqueta** que está verificando. Termina con un código distinto de cero si falla cualquier comprobación de firma, SBOM o procedencia, e indica cuál.

Termina con dos **controles negativos**. Un verificador que solo ha visto entradas correctas nunca ha demostrado que rechace nada, así que el script verifica una imagen firmada de DeviceChain contra una identidad que no puede coincidir, y verifica una imagen que no se firmó para esta versión (por defecto, una imagen anterior del operador de DeviceChain, de antes de que las versiones se firmaran), y exige que ambas se rechacen por el motivo correcto. Una ejecución que omita cualquiera de los dos no es un éxito.

## Verificar una imagen a mano {#by-hand}

```bash
TAG=v1.0.0
IMAGE=ghcr.io/devicechain-io/device-management:$TAG

cosign verify "$IMAGE" \
  --certificate-identity "https://github.com/devicechain-io/devicechain/.github/workflows/release.yml@refs/tags/$TAG" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

Ambas opciones importan. Sin la identidad, `cosign verify` aceptaría la firma de cualquiera que pueda firmar sin clave, es decir, de todo el mundo. Nombrar el flujo de trabajo **y la etiqueta** es lo que hace que la respuesta signifique «la versión `$TAG` de DeviceChain».

El SBOM y la procedencia:

```bash
# El SBOM, como atestación firmada; imprime la lista de paquetes.
cosign verify-attestation --type spdxjson "$IMAGE" \
  --certificate-identity "https://github.com/devicechain-io/devicechain/.github/workflows/release.yml@refs/tags/$TAG" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  | jq -r '.payload | @base64d | fromjson | .predicate.packages[].name' | sort -u

# Procedencia de la compilación.
gh attestation verify "oci://$IMAGE" --repo devicechain-io/devicechain \
  --signer-workflow devicechain-io/devicechain/.github/workflows/release.yml
```

El chart se verifica del mismo modo, con su propia etiqueta:

```bash
cosign verify ghcr.io/devicechain-io/charts/devicechain:1.0.0 \
  --certificate-identity "https://github.com/devicechain-io/devicechain/.github/workflows/release.yml@refs/tags/v1.0.0" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

## Fije por digest {#digest}

Una etiqueta es un puntero, y un registro puede moverla. `cosign verify` imprime el digest que verificó; despliegue **ese** (`imagen@sha256:...`) y lo que ejecuta es lo que comprobó. Si replica las imágenes en su propio registro, copie con ellas las firmas (`cosign copy`), o verifique antes de replicar y registre los digests.

Para rechazar imágenes sin firmar en la admisión en lugar de a mano, exija la misma identidad y el mismo emisor con una política de admisión como el [policy controller de Sigstore](https://docs.sigstore.dev/policy-controller/overview/).

## Qué le dice esto y qué no {#limits}

- **Origen, no idoneidad.** Una firma dice que el flujo de trabajo de versiones de DeviceChain produjo el artefacto. No dice que el código esté libre de defectos; para eso están las puertas de la versión y sus propias pruebas.
- **El SBOM se genera a partir de la variante `amd64` de la imagen.** La variante `arm64` se construye con el mismo código y las mismas dependencias, pero la atestación cubre el índice multiarquitectura completo y se produjo desde una sola plataforma.
- **La procedencia nombra al constructor, no el contenido.** Le permite confirmar el commit y el flujo de trabajo; no sustituye leer el SBOM.
- **La verificación confía en la infraestructura pública de Sigstore** (la autoridad certificadora y el registro de transparencia) y en los tokens de identidad de GitHub. Si no puede alcanzarlos desde donde verifica, verifique donde pueda y lleve los digests consigo.

Para informar de un problema con una firma, o de una vulnerabilidad en cualquier cosa verificada aquí, consulte la [política de seguridad](https://github.com/devicechain-io/devicechain/security/policy).
