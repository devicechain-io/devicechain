---
title: Almacenamiento de objetos
---

# Almacenamiento de objetos

Algunas cosas que guarda una plataforma no son ni filas ni puntos de series temporales: hoy, el **logo** de un inquilino y, más adelante, los paquetes de firmware. Son activos binarios opacos y no tienen cabida en la base de datos relacional. DeviceChain los guarda en un **almacén de objetos pluggable**: una sola interfaz en la biblioteca core compartida, con backends de almacenamiento que eliges por configuración.

El almacén de objetos es el hermano del [almacén de secretos encriptado](./architecture.md#secret-handling) y sigue el mismo diseño: una interfaz, muchos backends y una configuración tipada que rechaza en el arranque un backend desconocido o inválido en lugar de ignorarlo en silencio. La división de responsabilidades entre ambos es estricta. El almacén de objetos guarda solo activos binarios **no secretos**; las credenciales y demás secretos viven únicamente en el almacén de secretos con encriptación de sobre (envelope encryption), nunca aquí.

:::note Estado
**Disponible hoy:** la interfaz del almacén de objetos con dos backends, **filesystem** (el predeterminado) y **compatible con S3** (AWS S3 o MinIO) como reemplazo directo. El primer consumidor es el **white-labeling de inquilino** (logos de marca por inquilino).

**Planificado:** un backend de Google Cloud Storage; imágenes de marca más grandes, como fondos de inicio de sesión; paquetes de firmware/OTA y exportaciones de datos de inquilino, todo detrás de la misma interfaz. Este repositorio es la fuente de verdad de lo que se construye actualmente.
:::

## Una interfaz, muchos backends {#one-seam-many-backends}

Toda funcionalidad que guarda un activo binario pasa por la misma interfaz. Ninguna funcionalidad habla directamente con un SDK de almacenamiento.

- **Filesystem** (el predeterminado): los objetos viven en un volumen montado (un PVC en Kubernetes), sin ninguna dependencia de la nube. Funciona en un clúster kind local o en un despliegue autoalojado una vez que estableces `blob.directory` y habilitas `blobStorage.persistence` en el chart (un PVC montado en esa ruta). El chart lo deja sin configurar por defecto. Las lecturas se sirven a través de un **proxy de API que autoriza**; no hay ninguna ruta pública directa a los archivos.
- **S3 / compatible con S3**: AWS S3 o un **MinIO** autoalojado, con una sola API para ambos. Seleccionarlo es un cambio de configuración, no de código. Los backends en la nube también pueden emitir **URLs prefirmadas y con caducidad** para las lecturas. Las credenciales provienen de la cadena de credenciales estándar de la nube (entorno, identidad de carga de trabajo), nunca de un valor de configuración en texto plano.

Cada handle almacenado queda ligado al backend que lo escribió. Una instancia que ya guarda objetos en un backend debe migrarlos (o volver a subirlos) cuando cambia de backend.

Como todos los consumidores están detrás de la misma interfaz, un backend nuevo los beneficia a todos a la vez. Cambiar de backend es una decisión de despliegue y no una migración funcionalidad por funcionalidad: una sola migración de datos, no una por consumidor.

## Los objetos se referencian por handle

Un objeto almacenado se identifica mediante una **referencia opaca**. El consumidor persiste el handle en su propio registro (por ejemplo, el campo `branding logo` de un inquilino) y lo desreferencia cuando necesita los bytes. El handle no contiene datos; los bytes viven solo en el almacén.

Las claves de objeto llevan **prefijo de instancia y de inquilino**, de modo que los activos de cada inquilino quedan en un espacio de nombres separado del de cualquier otro. Cada segmento de la clave se valida estrictamente, así que en un backend basado en rutas una clave nunca puede salirse de su espacio de nombres.

**No hay ningún bucket público por defecto.** Cada lectura se autoriza a través del proxy de API o se sirve desde una URL firmada de corta duración que el servicio propietario emite de forma deliberada.

## Qué va en el almacén de objetos {#what-goes-here--and-what-doesnt}

| Dato | Dónde vive |
|---|---|
| Logos de marca hoy; paquetes de firmware y otros activos binarios a medida que lleguen | **Almacén de objetos** (esta página) |
| Contraseñas SMTP, tokens de webhook, credenciales de conectores | [Almacén de secretos encriptado](./architecture.md#secret-handling): encriptación de sobre, solo escritura, resuelto por handle |
| Telemetría y eventos de dispositivo | Hypertables de TimescaleDB, vía [event-management](./architecture.md#components) |
| Entidades (dispositivos, perfiles, paneles, …) | La base de datos relacional |

El sistema de registro relacional sigue siendo único y no pluggable por diseño. El almacén *binario* es el único aspecto del almacenamiento que es legítimamente pluggable, porque dónde vive físicamente un logo o una imagen de firmware es una preferencia de despliegue, no una decisión del modelo de datos.

## Despliegue

El filesystem predeterminado solo necesita un **volumen persistente**. Establece `blob.directory` y habilita `blobStorage.persistence`, y el chart de Helm crea el PVC y lo monta en los servicios que guardan activos. No hay infraestructura adicional que instalar ni operar.

Seleccionar el backend S3 es un cambio de configuración en la instancia. El endpoint y el bucket son configuración no secreta. La credencial de acceso se resuelve desde la cadena de credenciales del despliegue: por ejemplo, variables de entorno a partir del Secret de Kubernetes de la instancia, o la identidad de carga de trabajo en un clúster en la nube.

Como toda superficie de configuración de DeviceChain, la configuración del almacén de objetos es tipada y estricta: un nombre de backend mal escrito es un error de arranque, no un fallback silencioso. Un backend filesystem sin directorio se trata como "almacén de objetos no configurado". El servicio arranca igualmente, los endpoints de subida y lectura del logo devuelven **503**, y los logos en línea y por URL siguen funcionando.

## Primer consumidor: white-labeling

El white-labeling de inquilino es la primera funcionalidad construida sobre el almacén de objetos. El logo de un inquilino se sube al almacén y se referencia por handle desde la configuración de marca del inquilino. Los activos muy pequeños aún pueden suministrarse en línea (un data-URI acotado) en despliegues sin almacenamiento configurado, pero los activos de imagen reales pasan por el almacén. El campo `background` del registro de marca es un color hexadecimal, no una imagen.

La distribución de firmware/OTA, el caso principal de binarios grandes, está planificada sobre la misma interfaz.

## Relacionado

- **[Arquitectura](./architecture.md)**: dónde se sitúa la biblioteca core compartida, y el [almacén de secretos](./architecture.md#secret-handling) al que esta abstracción es paralela.
- **[Multitenencia](./multi-tenancy.md)**: el modelo de aislamiento de inquilinos que aplican las claves con prefijo de inquilino.
