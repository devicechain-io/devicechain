---
title: Geocercas
---

# Geocercas

Una **geocerca** es un contorno con nombre sobre la superficie de la Tierra. Los dispositivos reportan dónde están, y las reglas de detección preguntan si una posición reportada está dentro de una geocerca. Las dos mitades cambian con el tiempo — *dónde estaba el dispositivo* y *cuál era la geocerca* —, así que una respuesta honesta depende de comparar la versión correcta de cada una.

:::note Estado
Disponible: la autoría de geocercas en la consola (dibujar, editar, eliminar) o a través de GraphQL, contornos `POLYGON_2D` con huecos, contención esférica en las reglas de detección, y un archivo congelado de cada conjunto de geocercas con un historial navegable. Planeado: tipos de geometría adicionales — el esquema reserva `POLYGON_2_5D` y `VOXEL_3D`, ambos rechazados hoy en la escritura.
:::

## Dibujar una geocerca {#drawing-a-fence}

En la consola, las geocercas están en **Áreas → Geocercas**. Haz clic en el mapa para colocar un vértice, haz clic en el primer vértice para cerrar la forma, y guarda. Sobre una geocerca existente puedes:

- arrastrar un vértice para moverlo,
- hacer Alt-clic en un vértice para eliminarlo, o
- hacer clic en el pequeño tirador de una arista para añadir un vértice ahí.

Una geocerca lleva un **token** — el identificador con el que las reglas la nombran — más un nombre y una descripción opcionales. El token queda fijo una vez creada: la consola lo muestra como solo lectura al editar, y la API rechaza una actualización que lo cambiaría. Puedes cambiar el nombre cuando quieras, y cambiarlo no rompe nada.

El token es fijo porque una regla nombra una geocerca por su token, dentro de un texto de regla que la plataforma no puede reescribir por ti. Un renombrado dejaría cada `geo.inFence("old-token")` sin nombrar nada. Si necesitas otro token:

1. Crea la geocerca nueva.
2. Reapunta las reglas que nombran la antigua.
3. Elimina la geocerca antigua.

Ese orden te obliga a ocuparte de las reglas en lugar de descubrirlas más tarde.

### El mapa que hay detrás de tu geocerca {#no-default-basemap}

El editor dibuja sobre el **mapa base** del inquilino. Una instancia nueva ya viene con uno configurado, así que las teselas aparecen sin ninguna preparación.

Un administrador del inquilino cambia la fuente de teselas una sola vez, para todos, en **Configuración → Mapa**. Los campos de este editor son una anulación personal sobre ese valor, recordada únicamente en tu navegador, para probar un proveedor antes de aplicarlo a todo el inquilino. Consulta [Mapas base](./basemaps.md) para ver cómo se resuelven los niveles, por qué la URL de teselas y su atribución se mueven juntas, qué proveedor viene por defecto y cómo proteger la clave de API de un proveedor.

Si ningún nivel tiene una fuente de teselas —un operador definió el valor por defecto de la instancia como `{}` y el inquilino no definió nada—, el editor recurre a un mapa del mundo incorporado. Muestra continentes y fronteras de dominio público, compilados dentro de la consola, y nada con detalle de calle. Dibujar sigue funcionando, y las coordenadas que colocas son exactas en cualquier caso, porque usa la misma proyección que un mapa con teselas.

## Reglas de los contornos {#what-a-boundary-may-be}

Los contornos se almacenan como GeoJSON, con las posiciones en orden `[longitude, latitude]` y los anillos cerrados de forma explícita (la primera posición repetida como última). La consola se encarga de eso por ti; una integración que autora a través de GraphQL suministra el documento directamente.

Se aplican tres límites, y cada uno acota un costo distinto:

| Límite | Predeterminado | Qué acota |
| --- | --- | --- |
| Posiciones en una geocerca | 512 | Lo que cuesta compilar esa geocerca y mantenerla compilada |
| Geocercas por inquilino | 100 | El tamaño del anuncio de un cambio en el conjunto de geocercas |
| Posiciones en todo tu conjunto de geocercas | 51.200 | Cuánta geometría del motor de detección compartido ocupan tus geocercas |

El conteo de posiciones incluye la posición de cierre de cada anillo. El total del conjunto cuenta formas **distintas**: dos geocercas dibujadas de forma idéntica cuestan una forma, no dos.

Estos son **valores predeterminados**, no límites fijos de la plataforma. Cada uno forma parte de tu plan, y un operador puede subir o bajar cualquiera de ellos para tu inquilino. Los valores predeterminados son coherentes entre sí — 100 geocercas de 512 posiciones son exactamente 51.200 —, así que un inquilino al que nunca se le hayan cambiado puede alcanzar los tres.

Los límites se comprueban al guardar una geocerca, porque la puerta de costo de la autoría de reglas no puede ver un número que vive en una geocerca en lugar de en una regla. El número de geocercas no afecta a lo que tarda una comprobación de contención individual: una regla nombra una geocerca, y el motor llega a esa geocerca directamente.

### Guardar cuando superas un límite {#saving-when-you-are-over-a-limit}

:::note Cambiar una geocerca solo se rechaza cuando empeora las cosas
Si tu inquilino ya supera un límite, conservas todas las geocercas que tienes. Guardar solo se rechaza cuando el cambio haría el número **mayor** de lo que ya es.
:::

Tu inquilino puede superar un límite porque cambió un plan, o porque las geocercas son anteriores al límite. En ese estado:

- Editar el nombre o la descripción de una geocerca siempre funciona.
- **Eliminar** una geocerca siempre funciona.
- Hacer una geocerca *más pequeña* casi siempre funciona. La excepción: como el total del conjunto cuenta formas distintas, hacer diferente una de varias geocercas dibujadas igual la separa de las demás y puede subir tu total aunque esa geocerca se haya hecho más pequeña. El rechazo lo indica cuando ocurre.
- Eliminar baja el total almacenado, así que un inquilino por encima del límite que elimine una geocerca no puede volver a crearla. Para mover una geocerca a un token nuevo, **crea primero la nueva y elimina después la antigua**.

### Los anillos deben delimitar un área {#rings-must-bound-an-area}

Un contorno debe **delimitar un área**. Un anillo cuyas aristas se cruzan — un «moño» — no tiene un interior bien definido, así que una pregunta de contención sobre él no tiene respuesta honesta. Los anillos así se rechazan al guardar, mediante la misma comprobación que el motor de detección aplica cuando compila la geometría de un conjunto de geocercas.

Esto importa más de lo que parece. Antes de que la comprobación se ejecutara en el momento de la autoría, una geocerca así se guardaba sin problemas, quedaba en el registro con aspecto saludable, y fallaba solo más tarde, cuando por fin una regla la nombraba.

:::tip La consola advierte antes de que el servidor rechace
Mientras dibujas, la consola señala de inmediato una forma que se autointerseca. Su comprobación es una aproximación plana de la esférica que realiza el servidor, así que ambas pueden discrepar en geocercas muy grandes o en las que cruzan el antimeridiano. Por eso la consola solo *advierte*; la respuesta del servidor es la que decide.
:::

## Contención esférica {#the-maths-is-spherical}

La contención se calcula sobre una esfera, no sobre un mapa plano, y eso no es un refinamiento. Tratar la longitud y la latitud como `x` e `y` da respuestas equivocadas en dos lugares donde se sitúan geocercas reales:

- **A través del antimeridiano.** Una geocerca que va de 179°E a 179°O tiene 2° de ancho. Leída como coordenadas planas, se convierte en una banda de 358° de ancho que cubre casi todo el mundo — respondiendo «dentro» para un dispositivo en el Atlántico y «fuera» para uno que está dentro de la geocerca.
- **En latitudes altas.** El camino más corto entre dos puntos a la misma latitud se arquea hacia el polo, así que un «rectángulo» dibujado con cuatro vértices no está delimitado por líneas de latitud constante. En una caja que cubre 10°O–10°E y 80°–81°N, un punto a 80,05°N queda *fuera* de la geocerca real en el centro y *dentro* en los bordes. La matemática plana dice que ambos están dentro.

### Dónde cuenta el borde {#where-the-edge-counts}

Tres casos límite, cada uno decidido de una manera y aplicado de forma uniforme:

- **El borde está dentro.** Una posición que cae exactamente sobre la arista de una geocerca está contenida. Si se dejara a la biblioteca de geometría subyacente, el borde se repartiría entre regiones adyacentes, así que dos geocercas que comparten una arista reclamarían cada una una parte y ninguna reclamaría el resto. Una prueba explícita de «sobre la arista» lo evita, con una tolerancia de unos 6 mm sobre el terreno.
- **El borde de un hueco también está dentro**, por la misma regla. Una posición estrictamente dentro de un hueco está fuera de la geocerca; una posición sobre el anillo del hueco está dentro de la geocerca. Dos geocercas adyacentes, o una geocerca y su propio hueco, nunca discrepan sobre un punto que comparten.
- **El sentido del anillo da igual.** Las versiones en sentido horario y antihorario del mismo anillo dan la misma respuesta. El sentido se normaliza en lugar de darse por bueno, así que una integración que autora GeoJSON sobre la API no tiene que acertarlo. La única forma que esto no puede rescatar es una «geocerca» tan grande que envuelve casi todo el globo, donde «la región más pequeña» es ambigua. El límite de posiciones y la exigencia de delimitar un área dejan eso muy lejos de lo que parece una geocerca real.

## Versiones e historial del conjunto de geocercas {#fences-change}

Un cambio en el **conjunto** de geocercas — una geocerca creada, un contorno editado, una geocerca eliminada — **congela el conjunto entero de geocercas en una nueva versión**. Cada versión almacena la geometría de cada geocerca tal como estaba en ese momento, así que las formas se conservan tras ediciones y eliminaciones posteriores.

Renombrar una geocerca, o editar su descripción o sus metadatos, no crea una versión. Un nombre no cambia cómo se juzga ningún evento, así que una versión nueva congelaría exactamente las mismas formas que ya tiene la anterior. Por eso tu número de versiones puede no moverse tras guardar algo que claramente cambió.

Cada evento de ubicación queda sellado con la versión del conjunto de geocercas vigente cuando llegó. Ese sello es lo que hace significativo reproducir una regla sobre eventos pasados: los eventos de la semana pasada se juzgan contra las geocercas de la semana pasada. Sin él, una vista previa respondería a partir de las formas de hoy y sería silenciosamente ficticia — segura de sí, plausible, y sobre un mundo que nunca existió.

La pestaña **Historial** de una geocerca muestra el contorno tal como estaba en cualquier versión, con la forma actual dibujada detrás para comparar. Da una de tres respuestas:

| Lo que ves | Lo que significa |
| --- | --- |
| El contorno | La forma almacenada bajo este token en esa versión. |
| «No estaba en el conjunto en la versión *N*» | La geocerca no existía entonces — creada después, o eliminada y su token reutilizado. No es lo mismo que existir sin forma. |
| «Forma que este visor no puede dibujar» | Estaba en el conjunto y se aplicaba; solo la consola no puede renderizarla. |

Eliminar una geocerca es permanente y libera su token para reutilizarlo. Así que una entrada en una versión antigua te dice qué forma estaba almacenada bajo un token en ese momento — no necesariamente que perteneciera a la geocerca que estás mirando ahora.

## Usar una geocerca en una regla {#using-a-fence-in-a-rule}

Las reglas de detección alcanzan una geocerca por token:

```text
geo.inFence("yard-perimeter")
```

El predicado responde para la posición del evento que se está evaluando, contra el conjunto de geocercas con el que ese evento fue sellado.

Una regla que nombra una geocerca que no se puede compilar, porque su anillo no delimita un área, compila y publica igualmente. La geocerca se conserva en el conjunto con su error. En la evaluación, cada muestra contra ella se omite y se cuenta como error de evaluación en lugar de responderse de forma arbitraria — el mismo resultado que un token de geocerca desconocido, descrito [más abajo](#unknown-fence).

### Una prueba de geocerca y una de medición no pueden compartir condición {#fences-and-measurements}

Una condición que llama a `geo.inFence(...)` **y** lee una medición se rechaza al publicarla:

```text
geo.inFence("yard") && m["temp"] > 80    ← rechazada
```

No es una regla de estilo: ningún evento podría satisfacerla nunca. Un evento de ubicación reporta una posición y no lleva mediciones; un evento de medición lleva lecturas y no reporta posición. Una condición que necesita ambas no recibe nada, para siempre, y se quedaría en tu lista de reglas informando que está sana sin dispararse jamás. Publicar es el único punto donde las dos mitades se ven juntas; en la evaluación, cada una es solo una muestra que no cumplió los requisitos.

Exprésala en su lugar como dos reglas — una sobre la geocerca, otra sobre la medición. O, si el valor que pruebas cambia poco, ponlo en el dispositivo como un **atributo**, que un evento de ubicación sí lleva.

### Geocercas desconocidas o ausentes {#unknown-fence}

`geo.inFence("typo")` compila y publica: nada comprueba al publicar que el token nombre una geocerca real. En la evaluación la llamada no puede responder, y la plataforma no se inventa una respuesta. La muestra se **omite y se cuenta como error de evaluación**, nunca se responde «fuera». Responder «fuera» sería peor que inútil: para una regla que sostiene una condición en el tiempo, parecería que el dispositivo sale de la geocerca.

Cuatro situaciones lo producen, y solo la primera es un error:

| Situación | Lo que ves |
| --- | --- |
| El token está mal escrito, o la geocerca se eliminó | Errores de evaluación en cada evento de ubicación, desde que la regla entra en vigor |
| Previsualizar una regla sobre eventos **anteriores a que se dibujara la geocerca** | Errores durante todo el tramo previo a su existencia — la geocerca realmente no estaba en el conjunto entonces |
| Una regla autorada contra una geocerca que existe en una versión *posterior* a los eventos que se reproducen | Lo mismo, y por la misma razón |
| La primerísima regla de geocerca de un inquilino, en los segundos posteriores a publicarla | Transitorio; el motor carga el conjunto de geocercas al llegar la regla |

Los errores de evaluación aparecen solo en dos sitios:

- por regla, en la vista previa de autoría, como su contador de errores de evaluación;
- de forma agregada, en la métrica `devicechain_eventprocessing_detect_fanout_eval_errors_total` del motor de detección, que vigila la alerta `DetectFanoutEvalErrors` cuando las alertas de métricas del chart de Helm están habilitadas.

La vista de salud de reglas no los muestra, y una regla que produce errores en cada muestra sigue apareciendo ahí como `ACTIVE`. Nada señala la geocerca como causa. Si una regla de geocerca produce errores y nada más, revisa el token primero.

### Conjuntos de geocercas en memoria {#fence-set-retention}

El motor en vivo mantiene las **cuatro versiones más recientes del conjunto de geocercas** por inquilino: la actual más tres sustituidas. Está dimensionado para eventos aún en vuelo, que tienen segundos o minutos. Alcanzar el límite exige **cuatro cambios en el conjunto de geocercas mientras un evento va desde la ruta de ingesta hasta el motor**. Un evento sellado con una versión ya desalojada reporta el mismo error de evaluación contado que una geocerca desconocida.

No se pierde nada del historial cuando eso ocurre. La instantánea de cada versión se almacena de forma duradera, y las rutas de vista previa y reproducción leen de ahí en lugar de la caché en vivo. Solo la evaluación en vivo está acotada, y se recupera sola: los eventos siguientes llevan la versión actual.

El motor también vuelve a leer el conjunto de geocercas actual de cada inquilino desde el historial almacenado cada pocos minutos. Normalmente eso no cambia nada, porque una edición de geocerca se le anuncia al motor en el momento. Importa en un caso poco común: guardar una geocerca nunca falla porque no se haya podido enviar el anuncio, así que si ese anuncio se pierde, el motor seguiría evaluando contra la versión anterior hasta su siguiente reinicio. La relectura periódica cierra ese hueco, así que **una edición de geocerca surte efecto en unos pocos minutos como mucho, incluso si su anuncio nunca llega** — sin reinicio y sin necesidad de volver a guardar la geocerca.

Para saber cómo se autoran y evalúan las reglas de detección, consulta [procesamiento de eventos](./event-processing.md). Para los datos de ubicación en sí, consulta [conectar un dispositivo](../guides/connecting-a-device.md).

## Permisos {#permissions}

Leer las geocercas y su historial requiere `device:read`, y eliminar una requiere `device:write` — las mismas autoridades que gobiernan el resto del registro de dispositivos.

**Crear o cambiar una geocerca requiere además `location:read`.** Dibujar una geocerca no es solo una escritura: es una pregunta sobre dónde están los dispositivos. Quien pudiera crear geocercas solo con `device:write` podría colocar una pequeña, observar si alguna regla reacciona, moverla y deducir las posiciones de una flota a partir de las respuestas — sin haber tenido nunca la autoridad para ver una coordenada. Eliminar sigue requiriendo solo `device:write`, porque quitar una geocerca no pregunta nada.
