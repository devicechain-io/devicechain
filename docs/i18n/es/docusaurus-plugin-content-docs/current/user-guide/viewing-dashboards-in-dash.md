---
title: Ver paneles en /dash
sidebar_position: 17
---

import GuideShot from '@site/src/components/GuideShot';

# Ver paneles en /dash

Además de la consola, la instancia ofrece un visor de paneles independiente en `/dash/`. Muestra un
panel publicado y nada más: ni barra lateral, ni editor, ni borrador. Úsalo para una pantalla mural en
una sala de control, para un marcador que abra directamente un panel o para personas que solo
necesitan consultar un panel. Este capítulo es para cualquiera que lo abra y para quien prepare una
pantalla mural.

Los paneles se crean y se publican en la consola; consulta [Paneles](./dashboards.md).

## Iniciar sesión

El visor tiene su propio formulario de inicio de sesión, con **Correo electrónico**, **Contraseña**,
**Iniciar sesión** e **Iniciar sesión con SSO**, y un selector de idioma. Para el inicio de sesión único, escribe
primero tu dirección de correo y después elige **Iniciar sesión con SSO**. Si tu cuenta pertenece a
varios inquilinos, el visor te pide que elijas uno.

<GuideShot id="dash-signin" alt="La tarjeta de inicio de sesión de /dash con un campo de correo que contiene sam.rivera@widgetlab.example, un campo de contraseña vacío, un botón para iniciar sesión, otro para iniciar sesión con SSO y los botones de idioma EN English y ES Español." />

El visor guarda su sesión solo en la memoria de la página. No comparte la sesión de la consola: haber
iniciado sesión en la consola no te da acceso aquí, y cerrar sesión en uno no afecta al otro. Si
recargas la página, o abres `/dash/` en otra pestaña, tendrás que iniciar sesión de nuevo.

## Cargar un panel

Después de iniciar sesión, **Cargar un panel** lista **Tus paneles**: los paneles publicados que
puedes abrir. Elige uno para abrirlo. Si tu acceso está limitado a algunos de los clientes del
inquilino o a grupos de dispositivos, la lista contiene los paneles publicados compartidos con esos
clientes o grupos; si no, contiene todos los paneles publicados del inquilino. La lista muestra los 200 primeros, y lo indica cuando
hay más.

{/* TODO(screenshot): dash-list, the Your dashboards list above the token field. */}

Debajo de la lista, también puedes abrir un panel por su **Token del panel**, que puedes copiar de su
página en la consola: escríbelo y elige **Cargar publicado**. El visor obtiene la versión que se
publicó por última vez. Un panel que nunca se ha publicado no se puede abrir aquí; el
visor te pide que un autor lo publique.

<GuideShot id="dash-load" alt="La página Cargar un panel. El campo del token del panel contiene wl-gallery, con la pista de que obtiene la versión publicada por última vez, y un botón Cargar publicado. Debajo, bajo la opción de pegar una definición exportada, hay un cuadro para la definición en JSON, otro opcional para el manifiesto de vinculación con un ejemplo de su forma, y un botón Renderizar. La cabecera tiene los botones de idioma y el de cerrar sesión." />

La mitad inferior acepta en su lugar un panel que exportaste desde la consola: pega la definición y,
si quieres, un manifiesto de vinculación que apunte sus slots a dispositivos concretos, y elige
**Renderizar**. Consulta [Exportar](./dashboards.md#exporting) y, para los manifiestos de vinculación,
[Incrustación](../concepts/dashboards.md#embedding-definitions-slots-and-binding-manifests).

## El panel

El panel cargado ocupa la página bajo una cabecera estrecha. La cabecera muestra el título del
panel, el intervalo de tiempo y **Pausar** (consulta [Intervalo de tiempo y pausa](#time-range-and-pause))
y un indicador de conexión, y ofrece el selector de idioma, **Tema oscuro** (o **Tema claro**),
**Cambiar**, que vuelve al formulario de carga, y **Cerrar sesión**.

<GuideShot id="dash-view" alt="El panel Widget Lab en el visor /dash, con tema claro. La cabecera muestra Widget Lab, los intervalos 15m, 1h (seleccionado), 6h, 24h y 7d, un botón Pausar, un indicador En vivo y los botones de idioma, Tema oscuro, Cambiar y Cerrar sesión. Debajo: una tarjeta de título, una lectura de temperatura, un indicador de temperatura, un recuento de alarmas activas, una imagen, un gráfico de líneas de temperatura y humedad marcado 15m y una tabla de últimos valores." />

Los widgets están en vivo: los valores, los gráficos y las alarmas se actualizan a medida que llegan
datos. El indicador de conexión muestra **En vivo** mientras la conexión en vivo del panel está
activa; un dispositivo que simplemente no envía datos no lo cambia. Si se corta la conexión muestra
**Reconectando**, y **Sin conexión** cuando lleva 20 segundos caída, o de inmediato si el propio
navegador se queda sin conexión; los widgets se reanudan solos cuando vuelve. El visor no puede cambiar el panel, pero las acciones de los widgets siguen funcionando para
quien tiene permiso para usarlas. Quien tenga un rol con `alarm:write` puede reconocer y borrar
alarmas desde el panel, y quien tenga `command:write` puede enviar comandos a dispositivos reales. El
servidor comprueba esos permisos en cada acción. Consulta
[Acciones y permisos](../concepts/dashboards.md#acciones-y-permisos).

Los títulos del panel y de los widgets son texto que escribió el autor del panel, así que se quedan
tal como se escribieron, en el idioma que sea, aunque cambies el idioma del visor. En estas capturas
están en inglés. Algunos textos propios de los widgets, como los encabezados de las tablas o los
estados de las alarmas, también aparecen en inglés en el visor en español.

### Claro y oscuro

El botón de tema cambia el panel entre claro y oscuro, y este navegador recuerda tu elección para la
próxima vez.

<GuideShot id="dash-dark" alt="El mismo panel Widget Lab con el tema oscuro: fondos oscuros, texto claro, el botón de la cabecera que ahora dice Tema claro, y la lectura, el indicador, el recuento de alarmas, el gráfico y la tabla adaptados al fondo oscuro." />

## Intervalo de tiempo y pausa {#time-range-and-pause}

La cabecera lleva un intervalo de tiempo: **15m**, **1h**, **6h**, **24h** y **7d**. Fija cuánto
historial dibujan los gráficos, contado hacia atrás desde ahora. El panel se abre con el intervalo
que eligió su autor, una hora si no eligió ninguno, y el que elijas se aplica a todos los gráficos
que siguen el intervalo del panel. Un gráfico cuyo autor fijó su propio intervalo lo conserva, y lo
muestra en su esquina (por ejemplo **1h**) siempre que difiere del intervalo de la cabecera, para
que una hora fijada nunca se lea como un día de datos. Los intervalos largos se dibujan con puntos
promediados en lugar de cada lectura, de modo que una semana sigue cargando con rapidez.

Un gráfico guarda como máximo 2.000 lecturas. En un intervalo largo sobre un dispositivo con mucha
actividad eso puede no cubrir todo el intervalo: se descartan primero las lecturas más antiguas, y
el gráfico indica entonces en su esquina desde dónde empieza (por ejemplo **desde 14:03**), para que
una ventana parcial nunca se confunda con el intervalo completo.

**Pausar** congela el panel. Los valores, los gráficos, los contadores y las tablas de alarmas y los
mapas se quedan donde están, y junto a los controles aparece una insignia **En pausa** con la hora
a la que pausaste. En un panel congelado el tiempo sigue pasando: la antigüedad junto a un valor
(«hace 12 s») sigue contando desde la lectura que estás viendo, así que un valor en pausa nunca
parece actual. Elige **Reanudar** y el panel se pone al día con todo lo que llegó entretanto.
Durante la pausa puedes cambiar el intervalo; el panel muestra ese intervalo tal como estaba en el
momento en que pausaste.

La pausa congela lo que el panel muestra, no lo que hace. Los botones de comando siguen funcionando
en pausa, igual que la lista de comandos recientes que muestran; por eso la insignia **En pausa**
dice también «los comandos se siguen enviando»: un comando enviado desde un panel congelado sale en
el acto.

### Intervalo personalizado {#custom-range}

**Intervalo personalizado** abre un pequeño panel con los campos **Desde** y **Hasta**, para
mirar un tramo fijo del pasado, como las dos horas en torno a un incidente. Los campos usan tu zona
horaria, y el panel la muestra («Las horas están en Europe/Madrid (UTC+02:00)») para que «02:00» no
sea nunca ambiguo. **Aplicar** muestra el intervalo; se rechaza, con el motivo en el mismo sitio,
cuando el inicio no es anterior al fin, el intervalo dura menos de un minuto o más de 31 días, el fin
está en el futuro o el inicio es anterior al año 2000.

Mientras se muestra un intervalo personalizado, la cabecera indica **Intervalo fijo** con ambos
extremos y su zona horaria, y el panel no sigue los datos nuevos: gráficos y valores se quedan en
ese intervalo, igual que en pausa. Nada está en pausa, así que no hay insignia **En
pausa**, y los comandos se siguen enviando. **Volver a en vivo**, o cualquiera de los botones de 15m
a 7d, devuelve el panel a en vivo. **Copiar enlace** copia una dirección que abre el mismo intervalo
a cualquiera, con las horas escritas en UTC.

Un gráfico que sigue el intervalo del panel dibuja ese intervalo. Un gráfico cuyo autor fijó su
propio lapso lo conserva, contado hacia atrás desde el final del intervalo, y lo muestra en su
esquina como siempre. Un widget personalizado escrito antes de los intervalos personalizados ve el
intervalo como un panel congelado y dibuja los datos correctos, sin cambios.

Los widgets de alarmas, los mapas y los widgets de estado de dispositivo no guardan historial que
leer, así que no pueden mostrar el intervalo: muestran el estado actual, y lo indican con una
insignia **Ahora** en su esquina mientras se muestra un intervalo personalizado. Las alarmas activas
de hoy no son las que estaban activas durante el intervalo.

## Modo quiosco y enlaces para pantallas murales

La dirección del visor puede llevar parámetros, para que un marcador o una pantalla mural abra el
panel adecuado en el modo adecuado:

```text
/dash/?dashboard=<token>&kiosk=1&theme=dark
```

| Parámetro | Valores | Efecto |
| --- | --- | --- |
| `dashboard` | un token de panel | Carga ese panel justo después de iniciar sesión, también tras el inicio de sesión único. |
| `kiosk` | `1` o `true` | Modo quiosco: oculta la cabecera y sus controles, y muestra en su lugar un botón **Pantalla completa**. |
| `chrome` | `0` o `false` | Con `kiosk`, oculta también el indicador de conexión y el botón **Pantalla completa**, para una página que enmarca el panel por su cuenta. El indicador sigue apareciendo siempre que el panel no está en vivo. |
| `theme` | `dark` o `light` | Fija el tema. A diferencia del botón de tema, el parámetro no se recuerda. |
| `density` | `compact`, `comfortable` o `wall` | Escala el texto y el espaciado del panel: `compact` lo reduce y `wall` lo amplía, para una pantalla que se lee desde el otro lado de una sala. Sustituye la densidad con la que se guardó el panel. |
| `range` | `15m`, `1h`, `6h`, `24h` o `7d` | Abre el panel con ese intervalo de tiempo en lugar del propio del panel. |
| `paused` | `1` | Abre el panel congelado en el momento en que carga, con la insignia **En pausa** visible. Cualquier otro valor lo abre en vivo. |
| `from`, `to` | dos horas, cada una en ISO 8601 con zona, como `2026-10-22T02:00:00Z` o `2026-10-22T04:00:00+02:00` | Abre el panel en ese intervalo fijo en lugar de uno en vivo; consulta [Intervalo personalizado](#custom-range). Hacen falta las dos. Una hora sin zona se rechaza, porque «02:00» es un momento distinto en cada lugar. Prevalece sobre `range` y `paused`. |
| `bind.<slot>` | un token de dispositivo | Abre el panel con ese dispositivo elegido para el slot. Consulta [Enlazar a un panel con un dispositivo elegido](#linking-to-a-device). |

Si un parámetro lleva un valor que el visor no reconoce, se ignora, y los demás parámetros se siguen
aplicando.

Un par `from` y `to` que no se puede usar (solo uno de los dos, sin zona, el inicio no anterior al
fin, un intervalo fuera del rango de un minuto a 31 días o un fin en el futuro) se ignora, y una
nota bajo los controles del intervalo de tiempo indica los valores y el motivo, así que el panel
nunca muestra algo distinto en silencio. El panel se abre en vivo.

En modo quiosco el panel ocupa toda la página. El indicador de conexión está en la esquina superior
izquierda. El único control es **Pantalla completa**, en la esquina superior derecha, que pasa a ser **Salir de pantalla completa**; se oculta si el navegador no
puede pasar a pantalla completa. En modo quiosco no hay control para cerrar sesión, así que una
pantalla mural se queda en su panel. El modo quiosco tampoco tiene controles de intervalo ni de pausa,
pero un panel abierto con `paused=1` muestra igualmente su insignia **En pausa** junto al indicador de
conexión, de modo que una pantalla mural congelada lo dice.

<GuideShot id="dash-kiosk" alt="El panel Widget Lab en modo quiosco: sin cabecera, un indicador En vivo en la esquina superior izquierda, un botón de pantalla completa en la esquina superior derecha, y debajo la tarjeta de título y los mismos widgets en vivo." />

## Enlazar a un panel con un dispositivo elegido {#linking-to-a-device}

Un enlace también puede abrir un panel con un dispositivo ya elegido, nombrando el slot del panel y el
token del dispositivo:

```text
/dash/?dashboard=<token>&bind.machine=hauler-7
```

Repite `bind.<slot>` por cada slot que quieras fijar. El `<slot>` es el nombre del slot que le dio el
autor del panel. El visor muestra el panel solo después de comprobar, con tus propios permisos, que
cada dispositivo del enlace existe y es uno que puedes ver. Si no, muestra el motivo en lugar del
panel: un dispositivo que no existe y uno que no puedes ver se leen igual, de modo que un enlace no
sirve para averiguar qué dispositivos existen. **Abrir el panel sin él** abre entonces el panel con su
dispositivo predeterminado. Un enlace que nombra un slot que el panel no tiene, o algo que no es un
token de dispositivo, se rechaza del mismo modo. También se rechaza un enlace que nombra el mismo slot dos veces, y uno que nombra un slot cuyo
dispositivo sigue a otro slot, porque el panel mostraría el dispositivo que deduce y no el del enlace.

Elegir un dispositivo en el panel (hacer clic en un marcador de un mapa, en una fila de una tabla de
flota o en un selector) actualiza la dirección en el sitio, sin añadir entradas al historial del
navegador, de modo que la dirección de la barra es siempre un enlace a lo que se ve en pantalla. Al
volver **Atrás** o **Adelante** a una dirección que nombra otro dispositivo, se elige ese dispositivo.
**Cambiar**, que vuelve al formulario de carga, quita de la dirección los dispositivos elegidos. El
mismo enlace funciona en la consola, en `/dashboards/<token>?bind.<slot>=<token del dispositivo>`;
consulta [Elegir un dispositivo y compartir un enlace](/user-guide/dashboards#sharing-a-link).

## Mantener la sesión

Una pantalla mural puede funcionar días sin nadie delante, así que el visor renueva su sesión por sí
solo, un par de minutos antes de que caduque. Un corte de red breve, como una reconexión de Wi-Fi, no
termina la sesión: el visor sigue reintentando hasta que caduca su acceso actual.

Si la sesión no se puede renovar, porque el inicio de sesión se terminó en el servidor o el corte duró
demasiado, el panel se sustituye por un aviso a pantalla completa, **Tu sesión ha terminado**, con un
botón **Iniciar sesión de nuevo**. La pantalla nunca se queda congelada con valores antiguos. Si la
dirección lleva un parámetro `dashboard`, al iniciar sesión de nuevo vuelve directamente el mismo
panel.

Encontrarás más detalles en
[Renovación de sesión y modo quiosco](../concepts/dashboards.md#session-refresh-and-kiosk-mode).

## Relacionado

- [Paneles](./dashboards.md): crear, publicar y exportar paneles en la consola.
- [Paneles](../concepts/dashboards.md): widgets, fuentes de datos, versionado e incrustación.
- [Iniciar sesión](./signing-in.md): el inicio de sesión de la propia consola.
