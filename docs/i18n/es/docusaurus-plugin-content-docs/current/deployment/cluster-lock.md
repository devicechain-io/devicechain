---
sidebar_position: 2
title: El bloqueo del clúster
---

# El bloqueo del clúster

Toda ejecución de `dcctl` que cambie un clúster toma un **bloqueo** sobre ese clúster
antes de aplicar nada, y lo mantiene hasta que la ejecución termina. Dos operadores en
dos máquinas ya no pueden aplicar sobre el mismo clúster a la vez sin que a uno de los
dos se le avise.

Esta página es para quien acaba de recibir el aviso de que el clúster está tomado.

## «This cluster is claimed by…» {#claimed}

El mensaje aparece en dos formas, y la diferencia entre ellas es lo único que necesitas
de él al principio:

```
this cluster (working on instance "prod") is being worked on by
alice@build-01/48213/9f3c1a20b7e4d5c6, which renewed 4s ago; wait for it to finish
rather than running a second apply against the same cluster
```

Alguien está ejecutando `dcctl` contra este clúster **ahora mismo**. Espera a que
termine.

```
this cluster (working on instance "prod") is claimed by
alice@build-01/48213/9f3c1a20b7e4d5c6, which last renewed 6m12s ago — by this
machine's clock that looks stale, but a clock that disagrees looks the same. If that
process is genuinely gone, take the claim with `dcctl instances reclaim
--kube-context prod-cluster`, which checks properly before it steals
```

El bloqueo lleva un rato sin renovarse. Eso es **un indicio, no un veredicto**: la marca
de tiempo la escribió el reloj de otra máquina y la está leyendo el tuyo, así que los dos
pueden discrepar en más que el intervalo que estás mirando. Decidir si el titular
realmente se ha ido es trabajo de [`dcctl instances reclaim`](#reclaim), que usa una
prueba que no depende de que los dos relojes coincidan.

La cadena del titular nombra un **usuario**, una **máquina**, un **id de proceso** y un
sufijo aleatorio —`alice@build-01/48213/…`. Los tres primeros son cosas que puedes ir a
comprobar; el sufijo está ahí para que una segunda ejecución del mismo usuario en la
misma máquina no pueda confundirse con la primera.

### Qué comandos se niegan y cuáles avisan {#refuse-or-warn}

| Comando | Si el clúster está reclamado por otra persona |
|---|---|
| `dcctl bootstrap` | **Se niega**, en su segundo paso —antes de tocar el operador, la infraestructura o el chart. |
| `dcctl destroy` | Avisa y continúa —*«pero si esa ejecución está viva, esto peleará con ella»*. |
| `dcctl upgrade` | Avisa y continúa, con el mismo aviso. |
| `dcctl bootstrap --dry-run` | No toma ningún bloqueo, e informa del que *se habría* encontrado. |

(El bloqueo se toma en el *segundo* paso, no en el primero, porque el paso anterior es la
compilación de imágenes de la ruta de desarrollo `--build`, que no necesita el bloqueo del
clúster y produce la imagen que después despliega el paso del operador. En la ruta de
imágenes publicadas ese paso no hace nada en absoluto.)

La asimetría es deliberada. Un segundo arranque inicial ejecutándose junto al primero
produce una instancia construida mitad de cada uno, y negarse es la única respuesta útil.
Un desmontaje es algo que un operador ya ha decidido hacer, a menudo porque algo va mal, y
no poder preguntarle al clúster con educación primero no debe ser lo que se lo impida —así
que esos dos lo dicen alto y continúan.

Una ejecución en seco es un plan, y un plan que muta el clúster no lo es. No escribe nada,
el bloqueo incluido. Aun así informa de quién lo tiene, porque «ya hay otra persona
ejecutando» forma parte de la respuesta a «qué haría esto».

## Qué cubre realmente el bloqueo {#scope}

**Un bloqueo por clúster, no uno por instancia.** Casi todo lo que toca un arranque
inicial es un singleton de ámbito de clúster: el release de Helm, los releases de
infraestructura que instalan el controlador de ingress, cert-manager y el operador
CloudNativePG, y el propio Deployment del operador de DeviceChain. Dos ejecuciones
trabajando sobre dos instancias *distintas* seguirían sobrescribiéndose mutuamente todo
eso. El id de la instancia se registra en el bloqueo para que la negativa pueda decirte en
qué instancia está trabajando el titular, pero no es la clave del bloqueo.

:::caution Esto impone «una ejecución a la vez», no «una instancia por clúster»
El bloqueo impide que dos procesos `dcctl` apliquen a la vez. No hace que un clúster sea
apto para alojar dos instancias de DeviceChain: hoy un clúster aloja una, y eso sigue
siendo cierto haya o no alguien reteniendo el bloqueo.

De ese límite se encarga una comprobación distinta, un paso más tarde: el arranque
inicial pregunta al clúster si ya aloja una instancia *distinta* y se niega si es así,
diga lo que diga el bloqueo. Consulta [Una instancia por
clúster](./bootstrap.md#what-it-does).
:::

El bloqueo es un `Lease` de Kubernetes llamado `dcctl`, en el namespace donde el operador
de DeviceChain se instala a sí mismo (`dc-k8s-system`). Puedes leerlo directamente:

```bash
kubectl --context <kube-context> get lease dcctl -n dc-k8s-system -o yaml
```

Es válido durante **60 segundos** desde su última renovación, y el titular lo renueva cada
**10 segundos** —un margen lo bastante amplio como para que una llamada lenta a la API no
se confunda con un proceso muerto. Una ejecución que termina, falla o se interrumpe
**borra** el bloqueo al salir en lugar de dejarlo caducar, así que el caso ordinario no le
cuesta nada al siguiente operador.

### Permisos {#rbac}

`dcctl` actúa como la persona que lo ejecuta, así que en un clúster administrado por otra
gente tu cuenta necesita:

- `get`, `create`, `update` y `delete` sobre `leases.coordination.k8s.io` en
  `dc-k8s-system`;
- `get`, `create` y `update` sobre `instances.core.devicechain.io`, con alcance de clúster.

Si la cuenta no los tiene, `dcctl` te muestra la negativa del propio servidor de la API
—qué verbo, qué recurso, qué namespace, qué usuario— en lugar de presentarla como una
caída del servicio.

## Reclamar un bloqueo cuya ejecución ya no está {#reclaim}

Una ejecución que se mata sin poder devolver el bloqueo —un portátil perdido, una sesión
SSH caída, un OOM— lo deja atrás hasta que alguien lo toma.

```bash
dcctl instances reclaim --kube-context <kube-context>
```

`--kube-context` no es aquí una comodidad. Este comando existe para el operador que está
en una máquina *distinta* de la que hizo el arranque inicial, y esa máquina no tiene
registro local de la instancia, así que nombrar el clúster explícitamente es la única
forma que tiene de encontrar el bloqueo.

El comando imprime quién lo tiene, en qué instancia estaba trabajando y hace cuánto lo
renovó. Después te pide que **escribas de vuelta la identidad del titular**, exactamente:

```
  held by:   alice@build-01/48213/9f3c1a20b7e4d5c6
  instance:  prod
  renewed:   6m12s ago

Type the holder identity above to take the lock, or anything else to abort:
>
```

Cualquier cosa que no coincida aborta y deja el bloqueo en paz. Una pregunta de sí/no
añadiría ceremonia y ninguna información —la respuesta es la misma tanto si leíste la
línea de arriba como si no— y el único riesgo real de este comando es quitarle el bloqueo
a un proceso que sigue vivo. Escribir la identidad es lo que te obliga a mirar de quién
es.

Solo entonces comprueba:

```
checking whether the holder is still renewing (this takes about 1m0s)...
```

**La prueba es que nada tocó el bloqueo durante una ventana que cronometró esta máquina.**
Lee el objeto, espera una duración completa de arrendamiento medida en *tu* reloj, y lo
vuelve a leer. Un titular vivo renueva seis veces dentro de esa ventana, así que un objeto
que no ha cambiado en absoluto significa que ninguna renovación llegó al servidor de la
API. Nada compara los relojes de dos máquinas, porque cualquier comparación así se
equivoca en la magnitud de la desviación entre ambos —y la dirección peligrosa, declarar
muerto a un titular vivo, es la que produce gratis el reloj de un portátil a la deriva.

Si el titular despierta y renueva durante la ventana, o en el instante entre la
comprobación y la toma, la reclamación se **rechaza** en lugar de sobrescribir en silencio
una reclamación viva.

Al lograrlo, el bloqueo se devuelve de inmediato y el clúster queda libre:

```
the cluster lock is now free
```

`reclaim` no retiene el bloqueo por ti. Ejecuta tu `bootstrap`, `destroy` o `upgrade`
después, con normalidad.

:::danger No puede distinguir un proceso muerto de uno detenido
Una VM suspendida, la tapa de un portátil cerrada, un proceso detenido con `SIGSTOP`:
todos ellos se ven exactamente igual que una caída desde aquí, y ninguna cantidad de
espera cambia eso. Nada que una segunda máquina pueda observar distingue ambos casos.
**Confirma que el otro proceso realmente ya no está antes de quitarle el bloqueo**,
preguntando a la persona o mirando la máquina; este comando solo puede demostrar que nada
ha renovado, no que nada vaya a renovar.
:::

Deliberadamente **no hay `--yes`**. Una reclamación desatendida necesitaría un juicio —«sé
que ese proceso ya no está»— que ningún flag puede llevar consigo.

Si el bloqueo ya está libre, el comando lo dice y no hace nada:

```
this cluster is not claimed; there is nothing to reclaim
```

### Qué le pasa a la ejecución reclamada {#fenced}

Se entera. El titular vuelve a leer el bloqueo cada diez segundos y comprueba que sigue
siendo suyo, y `dcctl` lo vuelve a comprobar además en cada frontera entre pasos, así que
una ejecución reclamada se detiene antes de empezar su paso siguiente:

```
stopping before "Apply infrastructure": this cluster was reclaimed by another
operator: it is now held by bob@laptop/9912/3a7f…
```

Una ejecución así no escribe nada más en el clúster —ni siquiera la anotación de fase en
su propia declaración de instancia, porque esa declaración pertenece ya a quien la
reclamó.

:::caution Una reclamación no puede interrumpir un paso que ya está en marcha
La comprobación ocurre *entre* pasos. Una reclamación que llega un segundo después de
empezar «Apply infrastructure» no se atiende hasta que ese paso retorna, y una aplicación
de infraestructura puede durar decenas de minutos. La exposición es lo que queda del paso
actual, no el intervalo de detección de diez segundos —que es el verdadero motivo de que
una reclamación sea lenta, manual y escrita a mano en vez de automática.
:::

La simetría se sostiene también en la otra dirección: una ejecución que ha sido **incapaz
de renovar** durante una duración completa de arrendamiento —una partición de red, un
servidor de la API al que ya no llega— se declara perdida y se detiene, en lugar de seguir
aplicando con total confianza mientras otra persona concluye correctamente que ya no está.

## Interrumpir una ejecución {#interrupt}

`Ctrl+C` detiene una ejecución **con elegancia**. A la herramienta de infraestructura se
le pide que pare como ella quiere —termina la operación en curso y escribe su archivo de
estado— y `dcctl` devuelve el bloqueo antes de salir. La siguiente ejecución, que suele ser
la tuya reintentando, encuentra el clúster libre.

Como un solo release de Helm dentro de una aplicación puede llevar un timeout de varios
minutos, se permite que la parada elegante tarde: hasta veinte minutos en el peor caso, y
normalmente mucho menos.

**Un segundo `Ctrl+C` sale de inmediato.** Esa es la vía de escape para quien ha decidido
que esperar a una parada limpia ya no compensa.

:::caution La segunda interrupción renuncia a ambas protecciones
Termina el proceso sin la parada elegante, así que la herramienta de infraestructura puede
morir a mitad de una aplicación y perder la pista de recursos que acababa de crear —y el
bloqueo **no** se devuelve, de modo que la siguiente ejecución contra ese clúster tendrá
que esperar una duración de arrendamiento y [reclamarlo](#reclaim). Úsala cuando la
primera interrupción no esté avanzando, no como forma normal de parar.
:::

## Véase también

- [Arranque inicial de una instancia](./bootstrap.md) — qué hace cada paso de una
  ejecución.
- [Despliegue y operador](./kubernetes-operator.md#instance-declaration) — la declaración
  de instancia que escribe una ejecución, el finalizador que la protege y
  `dcctl instances release`.
