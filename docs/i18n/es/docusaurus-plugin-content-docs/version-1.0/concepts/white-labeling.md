---
title: Marca blanca e identidad de marca
---

# Marca blanca e identidad de marca

Un inquilino puede presentar la consola bajo su propia marca. Un logotipo, una paleta de colores y un título de producto reemplazan los valores por defecto de DeviceChain durante toda la sesión de consola del inquilino. La marca blanca (white-labeling) es parte del núcleo de código abierto, sin una edición separada, así que puedes ejecutar una sola instancia y dejar que cada inquilino cliente vea *su propia* marca.

:::note Estado
Disponible: la cascada de identidad de marca (inquilino → valor por defecto del operador → piso incorporado), el editor **Marca** de la consola, la herencia por campo y el almacenamiento de logotipos mediante el almacén de objetos o una referencia en línea/externa.
Planificado: una skin de pantalla de inicio de sesión por inquilino, un favicon y la resolución dominio personalizado → marca del inquilino. Hasta entonces, la página de inicio de sesión muestra la marca incorporada de DeviceChain, no el valor por defecto del operador. Consulta [Cascada de identidad de marca](#the-cascade).
:::

Aquí, marca blanca significa identidad de marca: apariencia y estilo. No es una bifurcación de la aplicación por inquilino. Los menús, los textos y las traducciones son los mismos para todos los inquilinos.

## Cascada de identidad de marca {#the-cascade}

La identidad de marca se resuelve **campo por campo** a través de una cadena de respaldo, del más específico al menos específico:

1. **Anulación del inquilino**: los propios campos de identidad de marca almacenados del inquilino.
2. **Valor por defecto del operador**: un valor por defecto a nivel de instancia que el operador establece como ajuste del sistema. Se aplica a todo inquilino que no haya anulado un campo.
3. **Piso incorporado**: la apariencia estándar de DeviceChain, compilada en la plataforma para que la cascada siempre se resuelva sin ninguna configuración.

Un inquilino que no establece nada hereda el valor por defecto del operador. Un operador que no establece nada obtiene el piso incorporado. Borrar un campo del inquilino hace que vuelva a heredarse, y el editor muestra, por cada campo, si el valor está establecido o heredado.

El servidor resuelve la cascada, así que todo cliente (la consola y los integradores) ve la misma identidad de marca efectiva.

La página de inicio de sesión no puede usar la cascada. No se conoce ningún inquilino antes de iniciar sesión, y la identidad de marca se aplica solo una vez seleccionado un inquilino, así que la página de inicio de sesión muestra la marca incorporada de DeviceChain.

## Campos personalizables {#what-is-customizable}

| Superficie | Campos |
|---|---|
| **Título** | el nombre del producto que se muestra en la pestaña del navegador (el título del documento) |
| **Logotipo** | una imagen (con un control de altura máxima) que se coloca en el encabezado de la consola |
| **Paleta** | cuatro colores —primario, fondo, primer plano, acento— aplicados como propiedades personalizadas de CSS en la raíz de la aplicación |

La consola se tematiza enteramente mediante tokens de diseño, así que la paleta es un único punto de escritura y no necesita CSS personalizado:

- El **primario** y el **acento** cambian el estilo de los tokens de diseño de la aplicación (botones, anillos de foco, acentos).
- El **fondo** y el **primer plano** recolorean únicamente el cromo de la barra lateral con marca. La base de la página conserva su tema claro/oscuro.

La inyección de CSS arbitrario no se ofrece de forma deliberada. Sería un riesgo de XSS y de mantenimiento con poca ganancia frente a una paleta adecuada.

## Almacenamiento del logotipo

Un logotipo es una referencia opaca, que se resuelve de una de tres maneras:

- **Subido**: almacenado en el [almacén de objetos](./object-storage.md) y servido de vuelta a través de una ruta proxy por inquilino que autoriza cada acceso, nunca una URL pública.
- **En línea (inline)**: un URI `data:` acotado (≤ 256 KB) guardado directamente en el registro de identidad de marca, para instalaciones sin infraestructura adicional.
- **URL externa**: un recurso `https://` que el inquilino aloja por su cuenta.

El servidor valida las subidas y las imágenes en línea antes de almacenarlas: solo tipos de imagen ráster, con límites de tamaño aplicados.

## Dónde vive la identidad de marca

La identidad de marca es un conjunto de columnas tipadas y anulables en el registro del plano de control del inquilino. No es un blob JSON, y **nunca está en el JWT**: los tokens solo transportan la autenticación.

La consola lee la identidad de marca resuelta a través de la consulta `tenant` con alcance propio, su consulta de arranque habitual. Almacena el resultado en caché por inquilino, con stale-while-revalidate: el valor en caché se pinta primero y luego una consulta nueva lo reemplaza en cada carga. Así, un cambio de marca se ve con prontitud.

La identidad de marca resuelta también lleva un `updatedAt`. Cambia cuando cambia *ya sea* la anulación del inquilino o el valor por defecto del operador, así que los clientes que mantienen su propia caché pueden indexarla con este valor.

## Editar la identidad de marca {#editing}

La identidad de marca se edita en la página **Marca** de la consola (plano del inquilino), que requiere la autoridad `branding:write`.

Los campos del tema (título, paleta, altura del logotipo) se guardan juntos como la anulación en bruto. El logotipo se gestiona por separado, con acciones que surten efecto de inmediato, así que reemplazar el tema nunca borra un logotipo subido.

Las mutaciones GraphQL correspondientes:

- **`setTenantBranding`** escribe la anulación del tema del propio inquilino del llamante. Un campo nulo borra ese campo, de modo que vuelve a heredarse.
- **`setTenantLogo`** establece o borra la referencia del logotipo. Las subidas pasan por un endpoint dedicado que escribe en el almacén de objetos.

Ambas actúan solo sobre el inquilino del token del llamante. Ambas validan su entrada y rechazan cualquier valor inválido antes de almacenarlo.

Consulta también [Multitenencia](./multi-tenancy.md) para el modelo de inquilino del que depende este registro, y [Almacenamiento de objetos](./object-storage.md) para dónde viven los recursos subidos.
