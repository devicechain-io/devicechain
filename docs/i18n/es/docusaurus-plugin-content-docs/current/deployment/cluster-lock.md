---
sidebar_position: 2
title: El bloqueo del clúster
---

# El bloqueo del clúster

Toda ejecución de `dcctl` que cambie un clúster toma un **bloqueo** sobre ese clúster
antes de aplicar nada, y lo mantiene hasta que la ejecución termina. Dos operadores en
dos máquinas ya no pueden aplicar sobre el mismo clúster a la vez sin que a uno de los
dos se le avise.

Esta página es para ti si una ejecución acaba de decirte que el clúster está tomado.

## Mensajes de clúster reclamado {#claimed}

El mensaje aparece en dos formas. Al principio, lo único que necesitas saber de él es cuál
de las dos has recibido.

```
this cluster (working on instance "prod") is being worked on by
alice@build-01/48213/9f3c1a20b7e4d5c6, which renewed 4s ago; wait for it to finish
rather than running a second apply against the same cluster
```

Alguien está ejecutando `dcctl` contra este clúster ahora mismo. Espera a que termine.

```
this cluster (working on instance "prod") is claimed by
alice@build-01/48213/9f3c1a20b7e4d5c6, which last renewed 6m12s ago — by this
machine's clock that looks stale, but a clock that disagrees looks the same. If that
process is genuinely gone, take the claim with `dcctl instances reclaim
--kube-context prod-cluster`, which checks properly before it steals
```

El bloqueo lleva un rato sin renovarse. Eso es **un indicio, no un veredicto**. La marca
de tiempo la escribió el reloj de otra máquina y la está leyendo el tuyo, así que los dos
pueden discrepar en más que el intervalo que estás mirando. Decidir si el titular
realmente se ha ido es trabajo de [`dcctl instances reclaim`](#reclaim), que usa una
prueba que no depende de que los dos relojes coincidan.

La cadena del titular nombra un usuario, una máquina, un id de proceso y un sufijo
aleatorio: `alice@build-01/48213/…`. Los tres primeros puedes ir a comprobarlos. El sufijo
evita que una segunda ejecución del mismo usuario en la misma máquina se confunda con la
primera.

### Qué comandos se niegan y cuáles avisan {#refuse-or-warn}

| Comando | Si el clúster está reclamado por otra persona |
|---|---|
| `dcctl install` | Se niega, antes de tocar el operador o los requisitos previos del clúster. |
| `dcctl bootstrap` | Se niega, en su segundo paso, antes de tocar la infraestructura o el chart. |
| `dcctl destroy` | Avisa y continúa: *«pero si esa ejecución está viva, esto peleará con ella»*. |
| `dcctl upgrade` | Avisa y continúa, con el mismo aviso. |
| `dcctl bootstrap --dry-run` | No toma ningún bloqueo, e informa de la reclamación que *se habría* encontrado. |

Un arranque inicial toma el bloqueo en su segundo paso, no en el primero. El primer paso es
la compilación de imágenes de la ruta de desarrollo `--build`, que no necesita el bloqueo
del clúster y produce las imágenes que después despliega el chart. En la ruta de imágenes
publicadas ese paso no hace nada en absoluto.

`dcctl upgrade` avisa en lugar de negarse por el bloqueo, pero tiene una negativa aparte
que no tiene nada que ver con el bloqueo. No moverá una instancia a una versión si el
clúster no tiene operador, o tiene uno identificablemente de otra versión, y nombra
`dcctl install` como el camino a seguir. Un operador instalado a mano se deja pasar con una
nota. Consulta [Versiones y actualizaciones](./releases-and-upgrades.md#zero-downtime-upgrades).

La asimetría es deliberada:

- **Install y bootstrap se niegan.** Un segundo arranque inicial ejecutándose junto al
  primero produce una instancia construida mitad de cada uno, así que negarse es la única
  respuesta útil.
- **Destroy y upgrade avisan.** Un desmontaje es algo que un operador ya ha decidido hacer,
  a menudo porque algo va mal. No poder preguntarle antes al clúster con educación no debe
  ser lo que se lo impida, así que estos dos lo dicen alto y continúan.
- **Una ejecución en seco no toma bloqueo.** Una ejecución en seco es un plan, y un plan
  que muta el clúster no lo es. No escribe nada, el bloqueo incluido. Aun así informa de
  quién tiene el bloqueo, porque «ya hay otra persona ejecutando» forma parte de la
  respuesta a «qué haría esto».

## Alcance del bloqueo {#scope}

Hay **un bloqueo por clúster, no uno por instancia**. Un clúster puede alojar varias
instancias, pero un arranque inicial también toca lo que comparten: la base de datos
relacional compartida, donde crea el login y la base de datos de la instancia. `dcctl
install` no toca *más que* lo compartido. El operador de DeviceChain y sus definiciones, el
controlador de ingress, cert-manager, el operador CloudNativePG y el resto los instala una
sola vez [`dcctl install`](./bootstrap.md#install), no cada arranque inicial, y por eso
precisamente ese comando toma el mismo bloqueo.

Dos ejecuciones trabajando a la vez sobre dos instancias *distintas* aplicarían ambas esa
mitad compartida, así que el bloqueo las pone en fila. El bloqueo registra el id de la
instancia para que la negativa pueda decirte en qué instancia está trabajando el titular,
pero ese id no es la clave del bloqueo.

:::note Esto impone «una ejecución a la vez»
El bloqueo impide que dos procesos `dcctl` apliquen a la vez. No es lo que mantiene
separadas las instancias: cada instancia tiene su propio namespace y su propio login de
base de datos, haya o no alguien reteniendo el bloqueo. Consulta [Varias instancias en un
mismo clúster](./bootstrap.md#what-it-does).
:::

El bloqueo es un `Lease` de Kubernetes llamado `dcctl`, en el namespace que ocupa el
operador de DeviceChain (`dc-k8s-system`). El namespace lo crea el primer comando que llegue
al clúster: `dcctl install` pone el operador en él, y un arranque inicial se asegura de que
exista para que el bloqueo siempre tenga dónde vivir. Puedes leer el bloqueo directamente:

```bash
kubectl --context <kube-context> get lease dcctl -n dc-k8s-system -o yaml
```

El bloqueo es válido durante **60 segundos** desde su última renovación, y el titular lo
renueva cada **10 segundos**. Ese margen es lo bastante amplio como para que una llamada
lenta a la API no se confunda con un proceso muerto. Una ejecución que termina, falla o se
interrumpe borra el bloqueo al salir en lugar de dejarlo caducar, así que el caso ordinario
no le cuesta nada al siguiente operador.

### Permisos {#rbac}

`dcctl` actúa como la persona que lo ejecuta. En un clúster administrado por otra gente, tu
cuenta necesita:

- `get`, `create`, `update` y `delete` sobre `leases.coordination.k8s.io` en
  `dc-k8s-system`;
- `get`, `list`, `create`, `update`, `patch` y `delete` sobre
  `instances.core.devicechain.io`, con alcance de clúster;
- `list` sobre `secrets` en `dc-system`.

`list` no es opcional en ninguna de las dos líneas, y es el verbo que más fácilmente se
omite. Cada bootstrap y cada upgrade preguntan al clúster qué instancias alberga ya y qué
han reclamado: el host de ingress, el puerto MQTT local, el presupuesto de conexiones. Esa
pregunta es un list, no un get. Una cuenta con solo `get` falla en la comprobación que
protege a las demás instancias del clúster. `update` y `delete` liberan el finalizer de la
declaración cuando se destruye una instancia, y `patch` registra la fase de una ejecución
en la declaración.

Si a tu cuenta le faltan estos permisos, `dcctl` te muestra la negativa del propio servidor
de la API (qué verbo, qué recurso, qué namespace, qué usuario) en lugar de presentarla como
una caída del servicio.

## Reclamar un bloqueo cuya ejecución ya no está {#reclaim}

Una ejecución que se mata antes de poder devolver el bloqueo (un portátil perdido, una
sesión SSH caída, un OOM) lo deja atrás hasta que alguien lo toma.

```bash
dcctl instances reclaim --kube-context <kube-context>
```

`--kube-context` no es aquí una comodidad. Este comando existe para el operador que está en
una máquina *distinta* de la que hizo el arranque inicial. Esa máquina no tiene registro
local de la instancia, así que nombrar el clúster explícitamente es la única forma que tiene
de encontrar el bloqueo.

El comando imprime quién tiene el bloqueo, en qué instancia estaba trabajando y hace cuánto
lo renovó. Después te pide que **escribas de vuelta la identidad del titular**, exactamente:

```
  held by:   alice@build-01/48213/9f3c1a20b7e4d5c6
  instance:  prod
  renewed:   6m12s ago

Type the holder identity above to take the lock, or anything else to abort:
>
```

Cualquier cosa que no coincida aborta y deja el bloqueo en paz. El único riesgo real de
este comando es quitarle el bloqueo a un proceso que sigue vivo. Una pregunta de sí/no
añadiría ceremonia y ninguna información, porque la respuesta es la misma tanto si leíste
la línea de arriba como si no. Escribir la identidad te obliga a mirar de quién es el
bloqueo.

Solo entonces comprueba el comando:

```
checking whether the holder is still renewing (this takes about 1m0s)...
```

La prueba es que **nada tocó el bloqueo durante una ventana que cronometró esta máquina**.
El comando lee el objeto, espera una duración completa de arrendamiento medida en *tu*
reloj, y lo vuelve a leer. Un titular vivo renueva seis veces dentro de esa ventana, así
que un objeto que no ha cambiado en absoluto significa que ninguna renovación llegó al
servidor de la API.

Nada compara los relojes de dos máquinas. Cualquier comparación así se equivoca en la
magnitud de la desviación entre ambos, y la dirección peligrosa (declarar muerto a un
titular vivo) es la que produce gratis el reloj de un portátil a la deriva.

Si el titular despierta y renueva durante la ventana, o en el instante entre la
comprobación y la toma, la reclamación se **rechaza** en lugar de sobrescribir en silencio
una reclamación viva.

Si sale bien, el bloqueo se devuelve de inmediato y el clúster queda libre:

```
the cluster lock is now free
```

`reclaim` no retiene el bloqueo por ti. Ejecuta después tu `bootstrap`, `destroy` o
`upgrade`, con normalidad.

:::danger No puede distinguir un proceso muerto de uno detenido
Una VM suspendida, la tapa de un portátil cerrada o un proceso detenido con `SIGSTOP` se ven
desde aquí exactamente igual que una caída, y ninguna cantidad de espera cambia eso.
**Confirma que el otro proceso realmente ya no está antes de quitarle el bloqueo**,
preguntando a la persona o mirando la máquina. Este comando solo puede demostrar que nada ha
renovado, no que nada vaya a renovar.
:::

Deliberadamente no hay flag `--yes`. Una reclamación desatendida necesitaría un juicio
(*«sé que ese proceso ya no está»*) que ningún flag puede llevar consigo.

Si el bloqueo ya está libre, el comando lo dice y no hace nada:

```
this cluster is not claimed; there is nothing to reclaim
```

### Qué le pasa a la ejecución reclamada {#fenced}

La ejecución reclamada se entera y se detiene antes de empezar su paso siguiente. El titular
vuelve a leer el bloqueo cada diez segundos para comprobar que sigue siendo suyo, y `dcctl`
lo vuelve a comprobar además en cada frontera entre pasos:

```
stopping before "Apply infrastructure": this cluster was reclaimed by another
operator: it is now held by bob@laptop/9912/3a7f…
```

Una ejecución cercada (reclamada) no escribe nada más en el clúster. Eso incluye la anotación
de fase en su propia declaración de instancia, porque esa declaración pertenece ya a quien
la reclamó.

:::warning Una reclamación no puede interrumpir un paso que ya está en marcha
La comprobación ocurre *entre* pasos. Una reclamación que llega un segundo después de
empezar «Apply infrastructure» no se atiende hasta que ese paso retorna, y una aplicación de
infraestructura puede durar decenas de minutos. La exposición es lo que queda del paso
actual, no el intervalo de detección de diez segundos. Ese es el verdadero motivo de que una
reclamación sea lenta, manual y escrita a mano en vez de automática.
:::

La misma protección funciona en la otra dirección. Una ejecución que ha sido **incapaz de
renovar** durante una duración completa de arrendamiento (una partición de red, un servidor
de la API al que ya no llega) se declara perdida y se detiene. No sigue aplicando con total
confianza mientras otra persona concluye correctamente que ya no está.

## Interrumpir una ejecución {#interrupt}

`Ctrl+C` detiene una ejecución con elegancia. `dcctl` le pide a la herramienta de
infraestructura que pare como ella quiere: termina la operación en curso y escribe su
archivo de estado. Después `dcctl` devuelve el bloqueo antes de salir, así que la siguiente
ejecución (normalmente la tuya, reintentando) encuentra el clúster libre. Un `SIGTERM`, que
es lo que envía un runner de CI o un planificador para cancelar un trabajo, se trata
exactamente igual que ese primer `Ctrl+C`.

Un solo release de Helm dentro de una aplicación puede llevar un timeout de varios minutos,
así que se permite que la parada elegante tarde: hasta veinte minutos en el peor caso, y
normalmente mucho menos.

**Un segundo `Ctrl+C` sale de inmediato.** Es la vía de escape para cuando has decidido que
esperar a una parada limpia ya no compensa.

:::warning La segunda interrupción renuncia a ambas protecciones
Termina el proceso sin la parada elegante, así que la herramienta de infraestructura puede
morir a mitad de una aplicación y perder la pista de recursos que acababa de crear. El
bloqueo tampoco se devuelve, de modo que la siguiente ejecución contra ese clúster tendrá que
esperar una duración de arrendamiento y [reclamarlo](#reclaim). Usa una segunda interrupción
cuando la primera no esté avanzando, no como forma normal de parar.
:::

### Cuando la parada elegante nunca vuelve {#abandoned}

Hay un tercer desenlace. Una ejecución que nadie está mirando (CI, un trabajo programado) es
la que llega a él, porque no hay nadie para pulsar `Ctrl+C` una segunda vez.

OpenTofu en sí obedece la parada. Pero algo que él arrancó, lo más habitual un plugin de
proveedor, puede sobrevivirle y seguir reteniendo la tubería de la que `dcctl` lee su
salida, así que `dcctl` nunca ve terminar el comando. En lugar de esperar para siempre,
`dcctl` desiste por su cuenta **alrededor de un minuto después del presupuesto de veinte
minutos** y sale con un error. Esto es lo que encontrarás en el registro:

```
dcctl stopped waiting for the interrupted OpenTofu command. OpenTofu itself has gone,
but something it started — a provider plugin, most likely — outlived it and is still
holding the pipe dcctl reads its output from, so dcctl cannot see the command end. It
was asked to stop gracefully and to write its state before this point and very probably
did, but nothing here witnessed that: treat this instance's infrastructure as PARTIALLY
APPLIED rather than untouched. Re-run the same command — the apply is idempotent and
reconciles whatever was left half done. Assuming nothing happened is the one reading
that is not safe (dcctl waited 21m0s after the interrupt)
```

De ahí se siguen dos cosas, y ambas son lo contrario de la segunda interrupción:

- **El bloqueo se devuelve.** Es un paso fallido, no una muerte del proceso, así que el
  bloqueo se libera como con cualquier otro error. No hay nada que [reclamar](#reclaim); la
  siguiente ejecución encuentra el clúster libre.
- **No se sabe que la infraestructura esté intacta.** A OpenTofu se le pidió que escribiera
  su estado y muy probablemente lo hizo, pero `dcctl` no fue testigo. Vuelve a ejecutar el
  mismo comando (`bootstrap`, `upgrade` o `destroy`, el que fuera) y deja que la aplicación
  reconcilie lo que quedó a medias. La única lectura que no es segura es «no pasó nada».

El reloj arranca con la interrupción y en ningún otro sitio. Una aplicación que nadie
interrumpió se espera lo que haga falta.

## Véase también

- [Arranque inicial de una instancia](./bootstrap.md) — qué hace cada paso de una
  ejecución.
- [Despliegue y operador](./kubernetes-operator.md#instance-declaration) — la declaración
  de instancia que escribe una ejecución, el finalizer que la protege y
  `dcctl instances release`.
