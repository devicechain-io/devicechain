---
title: Métricas calculadas
sidebar_position: 5
---

# Métricas calculadas

Una **métrica calculada** es una métrica de un perfil de dispositivo cuyo valor se obtiene de una
fórmula en lugar de ser enviado por el dispositivo. Un contador que envía `voltage` y `current`
puede tener una `power_kw` calculada con `voltage * current / 1000`, y desde ese momento `power_kw`
se almacena, se grafica, genera alarmas y se muestra en el dispositivo como cualquier medición que
este hubiera enviado. Este capítulo es para quienes crean perfiles de dispositivo. Explica qué es
una métrica calculada, cómo escribir una, el lenguaje de las fórmulas y qué ocurre cuando una
fórmula no puede producir un valor.

Las métricas calculadas están **desactivadas por defecto**. El operador de tu instancia las activa
dándole al nivel de tu inquilino un límite mayor que cero. Consulta
[Activar las métricas calculadas](#turning-calculated-metrics-on).

## Qué es una métrica calculada

- Una fórmula lee las métricas de **una lectura**: los valores **medidos** que un dispositivo envía
  juntos en una sola medición, otras métricas **calculadas** del mismo perfil (consulta [Encadenar
  métricas calculadas](#chaining-calculated-metrics)) y, opcionalmente, números que lleva el
  **propio dispositivo**, como un factor de calibración (consulta [Constantes del
  dispositivo](#device-constants)). Nunca lee una lectura anterior, otro dispositivo ni el reloj.
- Una **lectura** (una muestra) son todas las mediciones que un mensaje trae para el mismo instante,
  sin importar cómo las haya dividido el dispositivo en entradas. Un dispositivo que envía una
  métrica por entrada, como los dispositivos Sparkplug y LwM2M, también obtiene el cálculo de una
  fórmula sobre dos de esas métricas, siempre que compartan instante. Las entradas con instantes
  distintos son muestras separadas, y una fórmula nunca lee entre ellas. Si la misma métrica aparece
  dos veces en un instante, se usa y se conserva el primer valor.
- El resultado se **calcula una sola vez, cuando llega la lectura**, y se almacena como una medición
  normal junto a los valores que envió el dispositivo. Los paneles, el historial, las
  exportaciones, los últimos valores del dispositivo y las reglas de detección la leen como
  cualquier otra métrica, por su clave.
- **El historial nunca se reescribe.** Editar una fórmula y publicar el perfil cambia lo que se
  calcula para las lecturas que lleguen después. Los valores ya almacenados conservan la fórmula
  que estaba activa cuando se calcularon.
- Que una métrica sea medida o calculada **se fija al crearla**. Puedes editar la fórmula de una
  métrica calculada, pero no puedes convertir una métrica medida en calculada ni al revés. En ese
  caso, elimínala y crea una nueva.
- Un dispositivo no puede sobrescribir un valor calculado. Si un dispositivo envía un valor con la
  clave de una métrica calculada, ese valor se descarta y se conserva el calculado.

## Crear una métrica calculada

Abre un perfil de dispositivo y elige la pestaña **Métricas** (consulta
[Métricas](device-types-and-profiles.md#métricas)). Elige **Nueva métrica**.

1. Establece **Tipo** en **Calculada**. La elección es definitiva, por lo que queda bloqueada al
   editar una métrica.
2. Rellena la **Clave de la métrica**, el **Token** y el **Tipo de datos**: **Double**,
   **Integer** o **Boolean**. La fórmula debe producir exactamente ese tipo (consulta
   [Tipos](#types)). Las métricas calculadas no pueden ser cadenas de texto.
3. De forma opcional, define una **Unidad**, y un **Mín.** y un **Máx.** Un resultado fuera de ese
   rango se omite y no se almacena.
4. Escribe la **Fórmula**.
5. Elige **Crear métrica**.

Las métricas calculadas llevan una insignia **Calculada** en la lista de métricas.

Como cualquier edición de un perfil, una fórmula nueva o modificada se aplica a los dispositivos
solo después de que
[publiques una versión](device-types-and-profiles.md#publishing-a-version).

### Comprobar una fórmula mientras escribes

El cuadro de la fórmula pide al servidor que la compruebe mientras escribes, con el mismo
compilador que la comprueba al publicar y que la calcula con las lecturas reales. Debajo del cuadro
ves:

- el **error de compilación**, explicado con palabras, si la fórmula no es válida;
- el **coste estimado** frente al límite de 100. Una fórmula con un coste estimado superior a 100
  se rechaza;
- **advertencias**, que nunca impiden publicar. Por ejemplo, que la versión publicada declara esta
  clave como una métrica medida (de modo que los dispositivos pueden estar enviándola y esos
  valores se descartarán), que un resultado de magnitud 1e12 o mayor no se puede almacenar, o que
  las unidades no se convierten.

Cuando la fórmula compila, un panel de **muestra** enumera las métricas medidas que lee la fórmula.
Escribe un valor para cada una y el panel muestra el **valor** que produciría la fórmula o el
**motivo** por el que no produciría ninguno. Si la fórmula lee otras métricas calculadas, el panel
muestra también la **cadena** en la que se apoya y pide las métricas medidas que lee toda la
cadena: las calculadas se obtienen de tu muestra, en orden, exactamente como cuando llega una
lectura. Si la fórmula lee [constantes del
dispositivo](#device-constants), un grupo **Constantes del dispositivo** las enumera también:
escribe el número que contendrían los metadatos del dispositivo para cada una, o deja una en blanco
para ver qué obtiene un dispositivo que no la tiene. La muestra se escribe a mano y no se almacena
nada.

### Probar una fórmula con el historial reciente

Debajo de la muestra, **Probar con el historial reciente** ejecuta la fórmula sobre las lecturas
almacenadas de un dispositivo. Escribe un **Token del dispositivo**, elige una ventana (las últimas
1, 24 o 168 horas) y elige **Ejecutar sobre el historial**. El panel muestra cuántas muestras
evaluó, cuántas produjeron un valor, cuántas se omitieron y por qué, y las 50 muestras más recientes
con las entradas que leyó la fórmula y lo que dio. Una muestra son las lecturas que un mensaje
transportó para un instante, que es exactamente lo que ve una fórmula cuando llegan las lecturas. No
se almacena nada.

- Las entradas son los valores **almacenados**, que conservan 8 decimales (consulta
  [Almacenamiento y precisión](#almacenamiento-y-precisión)). El valor que se calcula cuando llega
  una lectura usa el valor del dispositivo con precisión completa, así que ambos pueden diferir para
  una entrada con más decimales: una corriente de `0.000000015` A se almacena como `0.00000002`. El
  panel lo indica en cada resultado, y lo que muestra es una vista previa, no el valor que se
  almacenará.
- Solo se muestran las métricas que lee la fórmula, y un mensaje que no llevaba ninguna de ellas no
  es una muestra. Una métrica calculada que lee la fórmula se toma del valor **almacenado** para
  ella cuando llegó cada lectura, con la fórmula publicada entonces, no se recalcula con el
  borrador.
- Si la fórmula lee [constantes del dispositivo](#device-constants), el panel usa para ellas los
  metadatos **actuales** del dispositivo indicado, en todas las muestras: no los valores que tenía
  el dispositivo cuando se registró cada lectura. Lo indica bajo el resultado. Una constante que no
  tienen los metadatos del dispositivo deja sus muestras en `missing_input`.
- Una ventana abarca como máximo 7 días. Una con más de 50.000 muestras, o más de 250.000 valores
  de entrada, se rechaza en lugar de recortarse. Elige una ventana más corta.
- Necesita permiso para leer los eventos del dispositivo. Un miembro limitado a algunos clientes ve
  solo las lecturas registradas mientras el dispositivo pertenecía a uno de ellos.

### Qué rechaza la comprobación al publicar

Al publicar un perfil se comprueban todas las métricas calculadas y se informa de todos los
problemas a la vez. Se rechaza un perfil cuando:

- una fórmula no compila o no produce el tipo declarado;
- una fórmula lee una clave que no es una métrica declarada del mismo perfil;
- unas métricas calculadas se leen entre sí en un ciclo (el mensaje lo nombra, por ejemplo
  `a -> b -> a`), o una métrica se lee a sí misma;
- una cadena de métricas calculadas tiene más de 4 eslabones (el mensaje nombra la cadena);
- una fórmula no lee ninguna métrica (una constante del dispositivo no es una métrica);
- una fórmula lee más de 8 constantes del dispositivo, o las fórmulas de un mismo perfil leen juntas
  más de 32;
- una fórmula supera los 1024 bytes, su coste estimado es superior a 100, o las fórmulas de un
  mismo perfil cuestan juntas más de 1000. Las fórmulas de una cadena se evalúan todas en cada
  lectura, así que una cadena cuesta la suma de sus fórmulas;
- el Mín. de una métrica calculada es mayor que su Máx., declara valores permitidos, o su tipo de
  datos no es Double, Integer ni Boolean;
- una clave está declarada dos veces;
- el perfil tiene más métricas calculadas de las que permite el nivel de tu inquilino.

## Métricas calculadas en la vista previa de una regla

Cuando previsualizas una regla de detección (**Vista previa contra el historial**, en el formulario
de una regla o en el lienzo de automatización), la vista previa usa las métricas calculadas del
**borrador** del perfil. A cada lectura reproducida se le recalculan sus métricas calculadas a partir
de sus valores medidos con las fórmulas del borrador, que sustituyen a los valores almacenados
cuando llegó. Por eso, una regla sobre una métrica calculada que acabas de añadir o editar se
previsualiza con los valores que tendrá esa métrica cuando publiques, y una métrica calculada que el
borrador ya no tiene no da ningún valor en la vista previa. La vista previa indica cuántas lecturas
se recalcularon y cuántos resultados de fórmula no produjeron valor. Las métricas calculadas que se
leen entre sí se recalculan en orden de dependencia, como cuando llega una lectura.

Una fórmula que lee [constantes del dispositivo](#device-constants) se recalcula con los
metadatos **actuales** de cada dispositivo, leídos cuando ejecutas la vista previa. Una lectura
almacenada con un valor anterior se previsualiza, por tanto, con el de hoy, y puede diferir del
valor que se almacenó entonces. Un dispositivo cuyos metadatos no tienen una constante no obtiene
ningún valor para sus lecturas. Si no se pueden leer las constantes, o el perfil tiene más de
10.000 dispositivos, el resultado lo dice en lugar de calcular con valores por defecto.

El recálculo lee los valores de cada lectura tal como los envió el dispositivo, con precisión
completa, así que da el mismo valor que calcularía la fórmula publicada para esa lectura. Hay dos
cosas que no puede reconstruir: un valor que se descartó cuando llegó la lectura (por ejemplo, uno
que un dispositivo envió con una clave que en ese momento era una métrica calculada) y los cambios
en las definiciones de las métricas medidas. Solo se recalculan los valores calculados. Nada de lo
que calcula una vista previa se almacena ni llega a las reglas activas.

## El lenguaje de las fórmulas

Las fórmulas se escriben en un lenguaje de expresiones pequeño. No tiene bucles, cadenas de texto
ni estado, de modo que cada fórmula se ejecuta en un tiempo pequeño y acotado.

### Entradas

Cada métrica medida del perfil es una variable con el nombre de su clave, como `voltage` o `temp`,
y también lo es cada métrica calculada que puede leer (consulta [Encadenar métricas
calculadas](#chaining-calculated-metrics)). Una clave que no es un identificador simple, como
`flow-rate`, se lee como `m["flow-rate"]`, envuelta en la conversión de su tipo:
`double(m["flow-rate"])`, `int(m["count"])` o `bool(m["door"])`.

### Operadores

| Tipo | Operadores |
| --- | --- |
| Aritméticos | `+` `-` `*` `/` `%` y el `-` unario |
| Comparación | `==` `!=` `<` `<=` `>` `>=` |
| Lógicos | `&&` `\|\|` `!` |
| Condicional | `condición ? a : b` |

### Funciones matemáticas

`math.abs(x)`, `math.floor(x)`, `math.ceil(x)`, `math.round(x)` (las mitades se redondean alejándose
de cero), `math.sqrt(x)`, `math.pow(x, n)`, `math.min(a, b)`, `math.max(a, b)` y
`math.clamp(x, bajo, alto)`.

`math.pow` acepta solo un **exponente entero**: `math.pow(x, 2)` funciona y `math.pow(x, 0.5)` no.
Las potencias fraccionarias pueden diferir en la última cifra entre arquitecturas de procesador, y
un valor almacenado no debe depender de qué máquina lo calculó. Para una raíz cuadrada, usa
`math.sqrt`.

### Presencia

`has(m.fan)` y `"fan" in m` comprueban si una lectura trae un valor para `fan`. Así le das un valor
por defecto explícito a una entrada ausente: `has(m.fan) ? fan : 0.0`.

### Encadenar métricas calculadas {#chaining-calculated-metrics}

Una fórmula puede leer otra métrica calculada del mismo perfil, por su clave, exactamente como lee
una medida. La potencia real, la potencia aparente y el factor de potencia forman una cadena de
tres:

| Clave | Fórmula |
| --- | --- |
| `kva` | `voltage * current / 1000.0` |
| `kw` | `kva * pf_in` |
| `pf` | `kw / kva` |

- **Una cadena tiene como máximo 4 métricas calculadas.** Una fórmula que solo lee métricas medidas
  es una cadena de una, `kw` arriba es de dos y `pf` de tres. Al publicar se rechaza una cadena más
  larga y se nombra. Las métricas calculadas tampoco pueden leerse entre sí en un ciclo, directamente
  o a través de otras, y al publicar se nombra el ciclo.
- **Se evalúan en orden de dependencia**, una vez por lectura, sea cual sea el orden en que las
  declaraste. Cada fórmula se ejecuta después de todo lo que lee.
- **Una fórmula posterior lee el valor que registra la lectura.** El resultado de `kva` se formatea
  con su tipo de datos antes de que lo lea `kw`, de modo que una métrica Integer es un número entero
  y un Boolean es `1` o `0`, y `kw` lee exactamente el valor que `kva` recibe en la lectura: el mismo
  que ven las reglas de detección y las vistas en vivo. Después, el almacenamiento lo redondea a 8
  decimales (consulta «Almacenamiento y precisión», más abajo), así que, para un valor con más
  decimales, el historial muestra un `kva` menos preciso que aquel con el que se calculó `kw`.
- **Si una métrica anterior no produce valor, tampoco lo producen las fórmulas que la leen.** Si
  `kva` se omite, `kw` se omite como `missing_input`, y también `pf` a través de ella. Para dar a
  una fórmula así un valor alternativo, comprueba la métrica como cualquier entrada:
  `has(m.kva) ? kva : 0.0`.
- **Un dispositivo no puede aportar un eslabón.** Un valor que un dispositivo envía bajo la clave de
  una métrica calculada se descarta, así que nunca llega a las fórmulas que leen esa clave.
- La identidad de una lectura no cambia: los valores calculados, encadenados o no, no forman parte
  de ella, así que una lectura reenviada se sigue reconociendo como la misma.
- Un valor encadenado se calcula a partir de la lectura y de la versión del perfil que estaba activa,
  como cualquier otro, así que la misma lectura da los mismos valores en todas las réplicas y en
  todas las vistas previas.

### Constantes del dispositivo {#device-constants}

Una fórmula puede leer un número que pertenece a un **dispositivo** y no a una lectura, como un
factor de calibración o la relación de un transformador de corriente. Se escribe como `device.`
seguido del nombre: `voltage * device.calibration`. Para un nombre que no sea un identificador
simple, escribe `device["ct-ratio"]`. El nombre `device` está reservado para esto: una métrica
medida cuya clave sea `device` se lee a través de `m`, envuelta en la conversión de su tipo, como
`double(m["device"])`.

El número procede de los **metadatos del dispositivo**. Edita el dispositivo y pon la constante en
sus metadatos como un número JSON con el mismo nombre:

```json
{ "site": "north plant", "calibration": 1.02 }
```

- **Nombrar una constante la declara.** No hay nada más que configurar en el perfil. Al publicar no
  se puede saber qué dispositivos llevan qué constantes; se comprueba que la fórmula compile y que
  use las constantes como números.
- Toda constante es un **double**. Un número entero es un double sin parte decimal, y
  `int(device.step)` lo lee como entero. Una fórmula que lee constantes pero ninguna métrica medida
  se rechaza.
- Una fórmula lee como máximo 8 constantes, y las fórmulas de un mismo perfil, como máximo 32 en
  conjunto.
- Una constante debe ser un **número JSON**. Un número escrito como texto (`"1.02"`), un booleano,
  `null`, un objeto o una lista no es un número y nunca se convierte.
- Como una entrada medida, una constante es **obligatoria** salvo que la fórmula la compruebe. Un
  dispositivo cuyos metadatos no la tienen se omite como `missing_input`, y uno que contiene algo
  que no es un número finito se omite como `invalid_input`. Para un valor por defecto,
  compruébala: `has(device.calibration) ? device.calibration : 1.0`, o
  `"calibration" in device`.
- **El valor queda fijado en el resultado.** La constante se lee cuando llega la lectura, y el valor
  calculado se almacena como cualquier otro. Editar los metadatos del dispositivo después cambia lo
  que se calcula para las lecturas que llegan tras el cambio (un servicio puede tardar unos
  segundos en verlo) y nunca cambia un valor ya almacenado.
- Cada dispositivo tiene sus propias constantes, así que dos dispositivos del mismo tipo pueden
  tener valores distintos, y la misma fórmula da a cada uno su propio resultado.

### Tipos {#types}

Una fórmula se comprueba contra el tipo de datos de la métrica, y las comprobaciones son estrictas:

- Un **entero** y un **double** se pueden mezclar en `+ - * /` y en las comparaciones de orden (el
  entero se promociona). No se pueden mezclar en `==` ni `!=`, ni en las dos ramas de
  `condición ? a : b`: escribe `80.0`, no `80`, al comparar con un double.
- Una fórmula para una métrica **Double** debe producir un double. Una fórmula que produce un
  entero necesita `double(...)`.
- Una fórmula para una métrica **Integer** debe producir un entero. Convierte un double con
  `int(...)`. `math.sqrt` y `math.pow` devuelven doubles, así que envuélvelas en `int()` para una
  métrica Integer.
- Una fórmula para una métrica **Boolean** debe producir un booleano, como
  `temp > 80.0 && fan == 0`. Los booleanos se almacenan como `1` y `0`.

### No disponible

Cadenas de texto (salvo como clave en `m["clave"]`), listas, mapas, tiempo y duraciones,
aleatoriedad, comprensiones, expresiones regulares y cualquier cosa que mire fuera de la lectura.

## Cuando una fórmula no produce valor

Si una fórmula no puede producir un valor para una lectura, esa métrica se **omite** para esa
lectura. El resto de la lectura se almacena con normalidad y nunca se sustituye nada: no hay valor
por defecto ni cero. Los motivos son:

| Motivo | Significado |
| --- | --- |
| `missing_input` | Una entrada que lee la fórmula no estaba en la lectura, o una constante del dispositivo que lee no está en los metadatos del dispositivo. Una entrada o constante es obligatoria salvo que la fórmula la compruebe con `has()` o `in`. |
| `invalid_input` | Una entrada estaba presente pero no es un valor válido para su tipo, o una constante del dispositivo está presente pero no es un número. |
| `non_finite` | Un número no era finito: una entrada o constante del dispositivo que no era un número finito, una división entre cero, un resultado demasiado grande para un double o la raíz cuadrada de un número negativo. |
| `div_zero` | Una división o un resto de enteros entre cero. |
| `overflow` | Un resultado o una conversión de enteros desbordó los 64 bits. |
| `range` | El resultado queda fuera del Mín. y el Máx. de la métrica. |
| `unstorable` | El resultado tiene una magnitud de 1e12 o mayor, con la que no se pueden almacenar las mediciones. |
| `eval_error` | Cualquier otro fallo, como `math.clamp` con sus límites invertidos. |

Una entrada ausente se detecta sea cual sea el orden de los operandos. `temp > 80.0 || fan > 1` se
omite en una lectura sin `fan`, aunque una lectura caliente hiciera verdadera la primera mitad. Si
quieres que la fórmula funcione sin `fan`, compruébala con `has()`.

## Almacenamiento y precisión

- Las mediciones se almacenan con un máximo de 12 dígitos antes del punto decimal y **8 decimales**.
  Un valor calculado se redondea a 8 decimales al almacenarse, y un resultado de magnitud 1e12 o
  mayor se omite como `unstorable`. Definir un Mín. y un Máx. dentro de más o menos
  999 999 999 999 le indica al editor que conoces el rango y elimina esa advertencia.
- Un valor calculado se almacena como cualquier medición, así que sigue la retención de mediciones
  de tu inquilino.
- Las unidades no se convierten ni se comprueban. Una fórmula lee sus entradas como números
  simples, y el resultado lleva solo la unidad que declares en la métrica calculada.
- Un valor calculado se identifica por su lectura, el nombre de su métrica y su instante, no por su
  valor. Si un mismo mensaje trae dos muestras para el mismo instante, producen una sola fila
  calculada por nombre de métrica, y se conserva el primer valor escrito.

## Activar las métricas calculadas {#turning-calculated-metrics-on}

El operador de la instancia define por **nivel** cuántas métricas calculadas puede publicar un
perfil, como **Métricas calculadas por perfil** en **Admin → Niveles**. El valor por defecto de la
plataforma es `0`, lo que significa que las métricas calculadas están desactivadas: puedes escribir
fórmulas y probarlas, pero publicar un perfil que tenga una se rechaza con:

> cannot publish device profile: it has N calculated metric(s), and calculated metrics are not
> enabled for this tenant's tier

Cuando el límite es mayor que cero pero inferior al número que tiene el perfil, el mensaje indica
cuántas permite el nivel por perfil. El máximo que puede conceder un nivel es 32. Pide a tu
operador que suba el límite si necesitas métricas calculadas.
