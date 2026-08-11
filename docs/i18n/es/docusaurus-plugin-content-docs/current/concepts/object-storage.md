---
title: Almacenamiento de objetos
---

# Almacenamiento de objetos

Algunas cosas que una plataforma guarda no son ni filas ni puntos de series temporales — el **logo** de un inquilino, una **imagen de fondo** de panel, y eventualmente paquetes de firmware. Estos son activos binarios opacos, y no pertenecen a la base de datos relacional. DeviceChain los almacena en un **almacén de objetos pluggable**: una interfaz en la biblioteca core compartida, con backends de almacenamiento intercambiables seleccionados por configuración.

Es el hermano del [almacén de secretos encriptado](./architecture.md#secret-handling), construido sobre la misma filosofía: una sola costura, muchos backends, configuración tipada que **falla de forma cerrada** — un backend desconocido o inválido se rechaza en el arranque, nunca se ignora silenciosamente. Y la división de responsabilidades entre ambos es estricta: el almacén de objetos guarda activos binarios **no secretos**; las credenciales y otros secretos viven únicamente en el almacén de secretos con encriptación de sobre (envelope-encrypted), nunca aquí.

:::note Estado
**Disponible hoy:** la abstracción de almacén de objetos con dos backends — **filesystem** (el predeterminado, un volumen montado/PVC conectado por el chart de Helm) y **compatible con S3** (AWS S3 o MinIO) como reemplazo directo. El primer consumidor es el **white-labeling de inquilino**: logos de marca e imágenes de fondo por inquilino. Backends adicionales (Google Cloud Storage) y consumidores adicionales (paquetes de firmware/OTA, exportaciones de datos de inquilino) están planificados sobre la misma interfaz — este repositorio es la fuente de verdad de lo que se construye actualmente.
:::

## Una costura, muchos backends

Cada funcionalidad que almacena un activo binario pasa por la misma abstracción — ninguna funcionalidad habla jamás directamente con un SDK de almacenamiento. Eso mantiene simple la narrativa de almacenamiento de la plataforma:

- **Filesystem** (el predeterminado) — los objetos viven en un volumen montado (un PVC en Kubernetes). Cero dependencia de nube: funciona en un clúster kind local y en despliegues autoalojados listos para usar. Las lecturas se sirven a través de un **proxy de API autorizante** — no hay ruta pública directa a los archivos.
- **S3 / compatible con S3** — AWS S3 o un **MinIO** autoalojado, una sola API cubriendo ambos. Un cambio de configuración, no un cambio de código. Los backends en la nube pueden además emitir **URLs firmadas y expirables (presigned)** para lecturas. Las credenciales provienen de la cadena de credenciales de nube estándar (entorno, identidad de carga de trabajo) — nunca de un valor de configuración en texto plano.

Debido a que cada consumidor se sitúa detrás de la única interfaz, un nuevo backend beneficia a todos a la vez, y cambiar de backend es una decisión de despliegue en lugar de una migración funcionalidad por funcionalidad.

## Los objetos se referencian por handle

Un objeto almacenado se identifica mediante una **referencia opaca** — el consumidor persiste el handle en su propio registro (digamos, el campo `logo de marca` de un inquilino) y lo desreferencia cuando se necesitan los bytes. El handle no porta ningún dato; los bytes viven únicamente en el almacén.

Las claves de objeto tienen **prefijo de instancia e inquilino**, de modo que los activos de un inquilino quedan aislados por espacio de nombres de los de otro, y cada segmento de clave se valida estrictamente — una clave nunca puede atravesar fuera de su espacio de nombres en un backend basado en rutas. No hay **bucket público por defecto**: cada lectura se autoriza a través del proxy de API o se sirve mediante una URL firmada de corta duración que el servicio propietario emite deliberadamente.

## Qué va aquí — y qué no

| Dato | Dónde vive |
|---|---|
| Logos de marca, imágenes de fondo, y otros activos binarios | **Almacén de objetos** (esta página) |
| Contraseñas SMTP, tokens de webhook, credenciales de conectores | [Almacén de secretos encriptado](./architecture.md#secret-handling) — encriptado por sobre, de solo escritura, resuelto por handle |
| Telemetría y eventos de dispositivo | Hypertables de TimescaleDB, vía [event-management](./architecture.md#components) |
| Entidades (dispositivos, perfiles, paneles, …) | La base de datos relacional |

El sistema relacional de registro permanece único y no pluggable por diseño; el almacén *binario* es la única preocupación de almacenamiento que es legítimamente pluggable, porque dónde vive físicamente un logo o una imagen de firmware es una preferencia de despliegue, no una decisión de modelo de datos.

## Despliegue

El predeterminado de filesystem solo necesita un **volumen persistente**, que el chart de Helm conecta para los servicios que almacenan activos — sin infraestructura adicional que instalar u operar. Seleccionar el backend de S3 es un cambio de configuración en la instancia: el endpoint y el bucket son configuración no secreta, mientras que la credencial de acceso se resuelve desde la cadena de credenciales del despliegue (por ejemplo, variables de entorno desde el Secret de Kubernetes de la instancia, o identidad de carga de trabajo en un clúster de nube).

Como toda superficie de configuración de DeviceChain, la configuración del almacén de objetos es **tipada y falla de forma cerrada**: un nombre de backend mal escrito o un backend de filesystem sin directorio es un error de arranque, no una alternativa silenciosa.

## Primer consumidor: white-labeling

El white-labeling de inquilino es la primera funcionalidad construida sobre el almacén de objetos: los activos de marca de un inquilino (logo, fondo) se suben al almacén y se referencian por handle desde la configuración de marca del inquilino. Los activos muy pequeños aún pueden suministrarse en línea (un data-URI acotado) para despliegues sin infraestructura de almacenamiento, pero los activos de imagen reales pasan por el almacén. La distribución de firmware/OTA — el caso de carga real para binarios grandes — está planificada sobre la misma costura.

## Relacionado

- **[Arquitectura](./architecture.md)** — dónde se sitúa la biblioteca core compartida, y el [almacén de secretos](./architecture.md#secret-handling) al que esta abstracción es paralela.
- **[Multitenencia](./multi-tenancy.md)** — el modelo de aislamiento de inquilinos que las claves con prefijo de inquilino aplican.
