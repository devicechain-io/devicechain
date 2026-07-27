---
sidebar_position: 10
title: Marca blanca e identidad de marca
---

# Marca blanca e identidad de marca

DeviceChain permite que un inquilino presente la consola bajo su propia marca:
un **logotipo**, una **paleta de colores** y un **título de producto** reemplazan
los valores por defecto de DeviceChain durante toda la sesión de consola del
inquilino. La marca blanca (white-labeling) es parte del núcleo de código abierto
—no existe una edición separada para ello— así que un operador puede ejecutar una
sola instancia y dejar que cada inquilino cliente vea *su propia* marca.

:::note Estado
Disponible: la cascada de identidad de marca (inquilino → valor por defecto del
operador → piso incorporado), el editor de **Identidad de marca** de la consola,
la herencia por campo, y el almacenamiento de logotipos vía el almacén de
objetos o una referencia interna/externa. Planificado (Fase 3): una **skin de
pantalla de inicio de sesión** por inquilino, un **favicon**, y la resolución
**dominio personalizado → marca del inquilino**; hasta entonces la página de
inicio de sesión muestra la marca del operador, ya que no se conoce ningún
inquilino antes de iniciar sesión.
:::

Marca blanca aquí significa **identidad de marca** —apariencia y estilo. No es
una bifurcación de la aplicación por inquilino: los menús, los textos y las
traducciones son los mismos para todos los inquilinos.

## La cascada

La identidad de marca se resuelve **campo por campo** a través de una cadena de
respaldo, del más específico al menos específico:

1. **Anulación del inquilino** — los propios campos de identidad de marca
   almacenados del inquilino.
2. **Valor por defecto del operador** — un valor por defecto a nivel de
   instancia que establece el operador (un ajuste del sistema), aplicado a todo
   inquilino que no haya anulado un campo.
3. **Piso incorporado** — la apariencia estándar de DeviceChain, compilada en la
   plataforma para que la cascada siempre se resuelva sin ninguna configuración.

Un inquilino que no establece nada hereda el valor por defecto del operador; un
operador que no establece nada obtiene el piso incorporado. Borrar un campo del
inquilino hace que vuelva a heredarse —el editor muestra, por cada campo, si el
valor está establecido o heredado. La cascada se resuelve **del lado del
servidor**, así que todo cliente (consola, integradores/embedders) ve la misma
identidad de marca efectiva.

## Qué es personalizable

| Superficie | Campos |
|---|---|
| **Título** | el nombre del producto mostrado en la pestaña del navegador y en el encabezado de la consola |
| **Logotipo** | una imagen (con un control de altura máxima) intercambiada en el encabezado de la consola |
| **Paleta** | cuatro colores —primario, fondo, primer plano, acento— aplicados como propiedades personalizadas de CSS en la raíz de la aplicación |

Dado que la consola se tematiza enteramente mediante tokens de diseño, la
paleta es un único punto de escritura: los cuatro colores reestilizan toda la
aplicación sin CSS personalizado. (La inyección de CSS arbitrario deliberadamente
no se ofrece —es una superficie de XSS y de mantenimiento con una ganancia
marginal frente a una paleta adecuada.)

## Almacenamiento del logotipo

Un logotipo es una **referencia** opaca, resuelta de tres maneras:

- **Subido** — almacenado en el [almacén de objetos](./object-storage.md) y transmitido de vuelta a través de una ruta proxy autorizadora por
  inquilino, nunca una URL pública.
- **En línea (inline)** — un `data:` URI acotado (≤ 256 KB) mantenido
  directamente en el registro de identidad de marca, para instalaciones sin
  infraestructura adicional.
- **URL externa** — un recurso `https://` que el inquilino aloja por su cuenta.

Las subidas y las imágenes en línea se validan del lado del servidor (solo
tipos de imagen ráster, con techos de tamaño aplicados).

## Dónde vive la identidad de marca

La identidad de marca es un conjunto de columnas tipadas y anulables en el
**registro del plano de control del inquilino** —no un blob JSON, y **nunca en
el JWT**. Los tokens permanecen solo-autenticación; la consola lee la identidad
de marca resuelta a través de la consulta `tenant` con alcance propio (su
consulta de arranque habitual) y la almacena en caché con
stale-while-revalidate, indexada por un `updatedAt` que se incrementa cuando
*ya sea* la anulación del inquilino *o* el valor por defecto del operador
cambian —así que un cambio de marca se propaga con prontitud.

## Edición

La identidad de marca se edita en la página **Identidad de marca** de la
consola (plano del inquilino), condicionada a la autoridad `branding:write`.
Los campos del tema (título, paleta, altura del logotipo) se confirman juntos
como la anulación en bruto; el logotipo se gestiona por separado con acciones
inmediatas, así que reemplazar el tema nunca borra un logotipo subido.

La superficie GraphQL correspondiente:

- **`setTenantBranding`** — escribe la anulación del tema del propio inquilino
  del llamante; un campo nulo borra ese campo (vuelve a heredarse).
- **`setTenantLogo`** — establece o borra la referencia del logotipo; las
  subidas pasan por un endpoint dedicado que escribe en el almacén de objetos.

Ambas tienen alcance propio limitado al inquilino en el token del llamante y se
validan de forma fail-closed antes de almacenar nada.

Consulta también [Multitenencia](./multi-tenancy.md) para el modelo de
inquilino del que depende este registro, y [Almacenamiento de objetos](./object-storage.md)
para dónde viven los recursos subidos.
