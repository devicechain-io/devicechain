---
sidebar_position: 16
title: Geocercas
---

# Geocercas

Una **geocerca** es un contorno con nombre sobre la superficie de la Tierra. Los dispositivos reportan dónde están; las reglas de detección preguntan si una posición reportada está dentro de una geocerca. Esa es toda la idea — pero sus dos mitades, *dónde estaba el dispositivo* y *cuál era la geocerca*, cambian con el tiempo, y obtener una respuesta honesta exige tener cuidado con qué versión de cada una se compara.

:::note Estado
Disponible: la autoría de geocercas en la consola (dibujar, editar, eliminar) o a través de GraphQL, contornos `POLYGON_2D` con huecos, contención esférica en las reglas de detección, y un archivo congelado de cada conjunto de geocercas con un historial navegable. Planeado: tipos de geometría adicionales — el esquema reserva `POLYGON_2_5D` y `VOXEL_3D`, ambos rechazados hoy en la escritura.
:::

## Dibujar una geocerca {#drawing-a-fence}

En la consola, las geocercas viven en **Áreas → Geocercas**. Haga clic en el mapa para colocar un vértice, haga clic en el primer vértice para cerrar la forma, y guarde. Sobre una geocerca existente puede arrastrar un vértice para moverlo, hacer Alt-clic en un vértice para eliminarlo, o hacer clic en el pequeño tirador de una arista para añadir un vértice ahí.

Una geocerca lleva un **token** — el identificador con el que las reglas la nombran — más un nombre y una descripción opcionales. El token queda fijo una vez creada.

### No hay mapa base predeterminado {#no-default-basemap}

El mapa se entrega **sin ninguna fuente de teselas configurada**, y eso es deliberado en lugar de una omisión: apuntar cada instancia autoalojada a un servicio público de teselas pondría a cada una de ellas en incumplimiento de una política de uso que nunca aceptó. Dibujar funciona sin ella — las coordenadas que usted coloca son exactas en cualquier caso — y puede establecer una URL de teselas y su atribución obligatoria en el editor, recordada únicamente en su navegador.

## Qué puede ser un contorno {#what-a-boundary-may-be}

Los contornos se almacenan como GeoJSON, con las posiciones en orden `[longitude, latitude]` y los anillos cerrados de forma explícita (la primera posición repetida como última). La consola se encarga de eso por usted; una integración que autora a través de GraphQL suministra el documento directamente.

Se aplican dos límites por conjunto de geocercas: como máximo **512 posiciones** en el conjunto de los anillos de una geocerca, contando la posición de cierre de cada anillo, y como máximo **100 geocercas** por inquilino. El primero existe porque la contención cuesta un tiempo proporcional al número de vértices en cada evento de ubicación, y la puerta de costo de la autoría de reglas no puede ver un número que vive en una geocerca en lugar de en una regla.

Un contorno debe además **delimitar un área**. Un anillo cuyas aristas se cruzan — un «moño» — no tiene un interior bien definido, así que una pregunta de contención sobre él no tiene respuesta honesta. Los anillos así se rechazan al guardar, mediante la misma comprobación que el motor de detección aplica cuando compila una regla. Eso importa más de lo que parece: antes de que la comprobación se ejecutara en el momento de la autoría, una geocerca así se guardaba sin problemas, quedaba en el registro con aspecto saludable, y fallaba solo más tarde, cuando por fin una regla la nombraba.

:::tip La consola advierte antes de que el servidor rechace
Mientras dibuja, la consola señala de inmediato una forma que se autointerseca. Su comprobación es una aproximación plana de la esférica que realiza el servidor, así que ambas pueden discrepar en geocercas muy grandes o en las que cruzan el antimeridiano — por eso la consola solo *advierte*. La respuesta del servidor es la que decide.
:::

## La matemática es esférica {#the-maths-is-spherical}

La contención se calcula sobre una esfera, no sobre un mapa plano, y eso no es un refinamiento. Tratar la longitud y la latitud como `x` e `y` da respuestas equivocadas en dos lugares donde de verdad se sitúan las geocercas reales:

- **A través del antimeridiano.** Una geocerca que va de 179°E a 179°O tiene 2° de ancho. Leída como coordenadas planas se convierte en una banda de 358° de ancho que cubre casi todo el mundo — respondiendo «dentro» para un dispositivo en el Atlántico y «fuera» para uno que está parado dentro de la geocerca.
- **En latitudes altas.** El camino más corto entre dos puntos a la misma latitud se arquea hacia el polo, así que un «rectángulo» dibujado con cuatro vértices no está delimitado por líneas de latitud constante. En una caja que cubre 10°O–10°E y 80°–81°N, un punto a 80,05°N queda *fuera* de la geocerca real en el centro y *dentro* en los bordes. La matemática plana dice que ambos están dentro.

## Las geocercas cambian, y el historial debe sobrevivirlo {#fences-change}

Todo cambio en una geocerca — crear, editar, eliminar, incluso renombrar una — **congela el conjunto entero de geocercas en una nueva versión**. Cada versión almacena la geometría de cada geocerca tal como estaba en ese momento, de modo que las formas se conservan incluso después de que las geocercas mismas se editen o eliminen.

Cada evento de ubicación queda sellado con la versión del conjunto de geocercas vigente cuando llegó. Esto es lo que hace significativo reproducir una regla sobre eventos pasados: los eventos de la semana pasada se juzgan contra las geocercas de la semana pasada. Sin ello, una vista previa respondería a partir de las formas de hoy y sería silenciosamente ficticia — segura de sí, plausible, y sobre un mundo que nunca existió.

La pestaña **Historial** de una geocerca muestra el contorno tal como estaba en cualquier versión, con la forma actual dibujada detrás para comparar. Son posibles tres respuestas, y significan cosas distintas:

| Lo que ve | Lo que significa |
| --- | --- |
| El contorno | La forma almacenada bajo este token en esa versión. |
| «No estaba en el conjunto en la versión *N*» | La geocerca no existía entonces — creada después, o eliminada y su token reutilizado. No es lo mismo que existir sin forma. |
| «Forma que este visor no puede dibujar» | Estaba en el conjunto y se aplicaba; solo la consola no puede renderizarla. |

Como eliminar una geocerca es permanente y libera su token para reutilizarlo, una entrada en una versión antigua le dice qué forma estaba almacenada bajo un token en ese momento — no necesariamente que perteneciera a la geocerca que está mirando ahora.

## Usar una geocerca en una regla {#using-a-fence-in-a-rule}

Las reglas de detección alcanzan una geocerca por token:

```text
geo.inFence("yard-perimeter")
```

El predicado responde para la posición del evento que se está evaluando, contra el conjunto de geocercas con el que ese evento fue sellado. Una regla que nombra una geocerca que no se puede compilar — porque su anillo no delimita un área — falla al compilar en lugar de responder de forma arbitraria.

Para saber cómo se autoran y evalúan las reglas de detección, vea [procesamiento de eventos](./event-processing.md). Para los datos de ubicación en sí, vea [conectar un dispositivo](../guides/connecting-a-device.md).

## Permisos {#permissions}

Leer las geocercas y su historial requiere `device:read`; crearlas, cambiarlas y eliminarlas requiere `device:write` — las mismas autoridades que gobiernan el resto del registro de dispositivos.
