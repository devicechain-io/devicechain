---
title: Interfaz de terminal de la instalación
description: Ejecuta dcctl install en una interfaz de terminal a pantalla completa que te guía por las comprobaciones, las opciones y una revisión antes de instalar nada.
---

# Interfaz de terminal de la instalación {#install-terminal-ui}

`dcctl install local --ui=tui` ejecuta la instalación del clúster en una interfaz de terminal a
pantalla completa. Muestra lo que hará la instalación, ejecuta en pantalla las comprobaciones
previas de solo lectura, te permite cambiar las opciones del clúster y te pide que apruebes el
plan exacto antes de instalar nada. Después muestra las etapas de la instalación mientras se
ejecutan y termina con el resultado y el siguiente comando que debes ejecutar.

```bash
dcctl install local --ui=tui
dcctl install local --ui=tui --kube-context prod
```

La interfaz de terminal es **opcional** en esta versión: sin `--ui`, `dcctl install` imprime,
pregunta y sale exactamente igual que siempre, en un terminal o fuera de él. Instala lo mismo en
ambos casos, con las mismas opciones, las mismas comprobaciones y el mismo código de salida;
consulta [Instalar el clúster](./bootstrap.md#install) para saber qué hace la instalación. Solo
cubre la instalación del clúster. `dcctl bootstrap` no cambia y sigue funcionando como texto.

## Cuándo se usa la interfaz de terminal {#when-used}

`--ui` admite uno de tres valores:

| Valor | Qué obtienes |
| --- | --- |
| `plain` (el predeterminado) | La salida de texto que `dcctl install` ha impreso siempre. |
| `tui` | La interfaz de terminal, cuando stdin, stdout y stderr son todos un terminal interactivo. En otro caso, la salida de texto, sin cambios. |
| `auto` | La interfaz de terminal en un terminal interactivo, y la salida de texto en todos los demás casos, incluidos `--yes` y `--dev`. |

Incluso con `--ui=tui`, obtienes la salida de texto, sin secuencias de escape ni preguntas nuevas,
cuando:

- stdin, stdout o stderr están redirigidos a un archivo o a una tubería;
- la variable de entorno `CI` está definida;
- `TERM` está vacía o vale `dumb` (en Linux y macOS);
- la consola de Windows no puede procesar secuencias de terminal virtual (el host de consola antiguo);
- pasas `--dry-run`, que siempre imprime la salida de ensayo de siempre.

Con `--ui=tui --yes`, o `--ui=tui --dev`, la interfaz de terminal **solo muestra el progreso**: no
hay pantallas para elegir ni para revisar, nada espera por ti, y sale cuando termina la
instalación.

`--ui=tui` no se puede combinar con `--output json`; se rechaza antes de que se ejecute nada.

## Qué inicia una instalación y qué no {#what-starts-an-install}

No se instala nada hasta que eliges **Approve and install** en la pantalla de revisión. Hasta
entonces, la interfaz de terminal solo lee: la pantalla de bienvenida muestra el plan, y las
comprobaciones previas son las mismas comprobaciones de solo lectura que ejecuta la instalación en
texto.

Salir antes de aprobar no cambia nada. Exit, Ctrl+C, una señal de interrupción o que la interfaz
de terminal se detenga antes de ese momento terminan el comando sin haber cambiado nada, con el
código de salida 1. Si la interfaz de terminal deja de funcionar después de que la instalación
haya empezado, la misma instalación continúa como salida de texto; nunca se inicia una segunda vez.

Aprobar la revisión es tu respuesta a todo lo que el plan necesita. En particular, si el clúster
kind local todavía no existe, aprobar también responde a la pregunta de crearlo, solo para ese
clúster y ese plan. Volver atrás y cambiar cualquier cosa crea un plan nuevo, que necesita sus
propias comprobaciones y su propia revisión.

## Las pantallas {#screens}

### Pantalla inicial {#splash}

La marca de DeviceChain y la versión de dcctl. No hace ningún trabajo. En un terminal local que lo
admite se anima durante un máximo de 1,2 segundos; en los demás casos es una marca estática o una
cabecera de una línea. Cualquier tecla la omite, y esa tecla no pasa a la pantalla siguiente.
Ctrl+C en la pantalla inicial sale. Pasa `--no-splash` para no mostrarla. Nunca se anima por SSH, y
nunca se muestra con `--yes`, `--dev`, `--dry-run` ni `--help`.

### Bienvenida {#welcome}

El alcance del comando ("This installs shared cluster prerequisites. Create an instance later
with dcctl bootstrap."), el destino, el tamaño, la topología y el origen de las imágenes tal como
los resolvió la línea de comandos, y las palabras "No changes have been made." Continue ejecuta
las comprobaciones previas.

### Comprobación previa {#preflight}

Las comprobaciones de solo lectura, que aparecen a medida que se ejecutan. Cada comprobación se lee
como una palabra: `PASS`, `WARN`, `FAIL`, `UNKNOWN`, `NOT NEEDED`, `NOT CHECKED` o `INFO`.
Desplázate a una comprobación con las flechas para ver su solución.

- **Recheck** vuelve a ejecutar las comprobaciones después de corregir algo.
- **Switch to Compact** (o de vuelta a Standard) muestra lo que significa el cambio y después
  vuelve a ejecutar todas las comprobaciones para el nuevo tamaño.
- **Choose another context** elige un clúster existente entre los nombres de tu kubeconfig.
- Mientras alguna comprobación falla, **Continue no está disponible**, y no hay forma de saltárselo
  en pantalla. `--skip-preflight` en la línea de comandos sigue siendo la única forma de omitir las
  comprobaciones.

#### Comprobaciones del clúster existente {#preflight-advisory}

Con `--kube-context`, un grupo "Existing cluster (advisory)" sigue a las comprobaciones. Hace al
clúster preguntas de solo lectura: si el nombre del servidor de la API se resuelve, si responde con
un certificado TLS en el que confía el contexto, si se aceptan las credenciales del contexto y si
pueden crear lo que crea la instalación. También pregunta si esta máquina llega a los registros de
proveedores, charts e imágenes de los que descarga este plan. Estas comprobaciones son un consejo:
un problema se lee como `WARN`, lo que no pueden decidir se lee como `UNKNOWN` o `NOT CHECKED`, y
nunca bloquean Continue.

- **Plugins de credenciales.** Un contexto que obtiene sus credenciales de un plugin exec (como
  suelen hacerlo los contextos de GKE, EKS y AKS) se autentica ejecutando el plugin, como lo haría
  kubectl: nunca de forma interactiva, sin terminal y sin nada en su entrada, durante 10 segundos
  como máximo, y se detiene junto con todo lo que haya iniciado cuando se agota ese tiempo o sales
  de la pantalla. La credencial que imprime solo se envía al servidor de la API del contexto y nunca
  se muestra. Un plugin que debe ejecutarse de forma interactiva (`interactiveMode: Always`) no se
  ejecuta y se lee como `NOT CHECKED`; un plugin que no está instalado se lee como `WARN`; uno que
  falla, se cuelga o no imprime una credencial utilizable se lee como `UNKNOWN`. Lo que el plugin
  imprime como error nunca se muestra, porque puede contener un token o una clave: la fila solo
  dice cómo falló el plugin, y `kubectl auth whoami` contra el contexto muestra su propio mensaje.
  Las entradas heredadas `auth-provider` nunca se ejecutan.
- **Registros.** Solo se comprueban los repositorios de charts de los componentes que instala este
  plan, así que Compact (que deja fuera la monitorización) y las opciones de monitorización y de
  CloudNativePG del panel Advanced cambian la lista. Los charts de los controladores de ingress
  siguen al clúster: siempre el de Traefik, y el de ingress-nginx cuando el clúster ya ejecuta
  ingress-nginx (que la instalación conserva salvo que se indique `--remove-legacy-ingress`), según
  las IngressClasses del clúster.
- **Proxies.** Estas comprobaciones nunca pasan por un proxy. Cuando hay uno configurado, para el
  contexto o en el entorno (`HTTPS_PROXY` y similares), las filas que pasarían por él se leen como
  `NOT CHECKED`.

### Opciones del clúster {#cluster-choices}

El destino (un clúster kind local gestionado, al que puedes cambiar el nombre, o un contexto
existente), el tamaño (Standard o Compact) y la topología (Single o HA). Un valor que fijaste en la
línea de comandos se marca con la opción que lo fijó. Lo que Compact deja fuera se explica debajo.

**Advanced** abre un panel con editores para la pila de monitorización, el operador CloudNativePG
incluido y, con Compact, TLS. Indica si se harán copias de seguridad de la base de datos, tal como
lo decide el plan. Cualquier otra opción que indicaste en la línea de comandos aparece ahí por su
nombre y se aplica a la instalación sin cambios. Las credenciales solo se muestran como
"configured (hidden)".

Cada cambio se valida igual que lo haría la línea de comandos, de modo que una combinación que la
línea de comandos rechaza también se rechaza en pantalla, y las comprobaciones de solo lectura
vuelven a ejecutarse para el plan modificado.

### Revisión {#review}

El plan tal como lo resolvería la línea de comandos: el destino y si se crea o se reutiliza, la
versión y el registro de imágenes, el tamaño y la topología, TLS, la monitorización, las copias de
seguridad, las advertencias y dónde se guardará el estado del clúster. También muestra el
**comando equivalente**: el comando `dcctl install` no interactivo, con `--yes`, que reproduce este
plan. Una credencial como un webhook de alertas solo aparece en él como un marcador, y el comando se
marca entonces como plantilla.

La revisión se abre con **Back** seleccionado, y las teclas pulsadas antes de que se haya
estabilizado se ignoran, de modo que un Enter de más nunca puede aprobarla.

### Progreso {#progress}

Las etapas de la instalación, cada una con una palabra de estado (`DONE`, `RUN`, `WAIT`, `FAIL`,
`STOP`) y su propia barra. Una barra solo es una medida cuando hay algo real que contar, como las
acciones de OpenTofu o las réplicas listas (`2/3`); en otro caso, un marcador indica que hay trabajo
en curso. La barra general cuenta las etapas completadas de entre las que se aplican. Nunca es un
porcentaje de la instalación ni una estimación del tiempo restante, que siempre se lee
`Remaining: unknown`. Si no se ha observado nada nuevo durante un rato, la pantalla dice que el
trabajo sigue en marcha; no lo llama bloqueado. Después de la última etapa se lee `Finalizing` hasta
que la instalación informa de su resultado.

Pulsa `l` para abrir el panel de registro, que muestra la salida de la instalación sin
credenciales. PgUp y PgDn lo desplazan y lo pausan; `f` o End vuelven a seguirlo.

**Ctrl+C durante la instalación** la detiene de forma segura: la pantalla dice "Interrupt received.
Waiting for the operation to stop safely. Nothing is being rolled back." y permanece hasta que la
instalación se ha detenido. Un segundo Ctrl+C sale de inmediato sin esperar, como hace la
instalación en texto. Esc nunca detiene la instalación.

### Resultado {#result}

- **Éxito** dice "Shared prerequisites installed", con el clúster, su forma, TLS, la
  monitorización, las copias de seguridad y el presupuesto de conexiones. Un clúster no tiene una
  consola propia, así que no se muestra ninguna. Lo siguiente es el comando `dcctl bootstrap` que
  crea una instancia en él. Done sale con 0.
- **Fallo** indica la etapa que falló y su causa, lo que puede haber cambiado, si se liberó el
  bloqueo del clúster, un comando para inspeccionar el clúster y la solución. Una instalación
  converge, así que la solución es corregir la causa y **volver a ejecutar el mismo comando**; se
  muestra el comando para volver a ejecutarla. **Review for re-run** lleva la misma petición de
  vuelta a la revisión, una vez que la ejecución fallida se ha detenido del todo. No se deshace
  nada, y cerrar la pantalla de fallo nunca convierte el resultado en un éxito: dcctl sale con 1.
- Una instalación que quedó **registrada pero cuyas réplicas todavía se estaban uniendo** cuando se
  agotó la espera de disponibilidad se muestra como un fallo que lo dice, nunca como un éxito.

Al salir dcctl, el terminal se restaura y se imprime un breve resumen: el resultado, el destino y el
siguiente comando. No contiene credenciales.

## Teclas {#keys}

| Tecla | Antes de la instalación | Durante y después de la instalación |
| --- | --- | --- |
| Tab / Shift+Tab | Moverse entre campos y acciones | Moverse entre las acciones de la pantalla de resultado |
| ↑ / ↓ | Elegir una opción, o moverse entre comprobaciones | — |
| Space | Activar o desactivar una opción | — |
| Enter | Ejecutar la acción seleccionada | Ejecutar la acción seleccionada en la pantalla de resultado |
| `y` / `n` | — | Responder a una pregunta que la instalación hace en pantalla (una ejecución que solo muestra el progreso) |
| Esc | Volver a la pantalla anterior (tus cambios se conservan) | Cerrar la ayuda. Nunca detiene la instalación |
| `?` / F1 | — | Ayuda, en la pantalla de resultado |
| `l`, PgUp / PgDn, `f` / End | — | Abrir el panel de registro, desplazarlo, seguirlo |
| `q` | Ir a Exit | Salir, una vez terminada la instalación |
| Ctrl+C | Salir; no se ha cambiado nada | Detener la instalación de forma segura; una segunda pulsación sale de inmediato |

El pie de cada pantalla enumera las teclas que funcionan en ella. El ratón no se captura, así que
seleccionar y copiar texto con el ratón funciona como en cualquier terminal. El control
seleccionado se marca con `›` y se muestra invertido, nunca solo con color.

## Terminales y accesibilidad {#accessibility}

La interfaz de terminal se adapta a lo que puede hacer el terminal. Cada estado se escribe como una
palabra, así que nada depende del color:

| Terminal | Qué ves |
| --- | --- |
| Color verdadero, Unicode | Color completo, la marca de DeviceChain, barras dibujadas con bloques |
| 256 colores, Unicode | Lo mismo, con la paleta de 256 colores más cercana |
| 16 colores, o Unicode no garantizado | Bordes y barras ASCII, una cabecera de texto en lugar de la marca, un marcador `working` estático |
| `NO_COLOR` definida | Sin ningún color, una cabecera de texto en lugar de la marca; las palabras transmiten cada estado, y nada se mueve |

`NO_COLOR` tiene prioridad sobre `CLICOLOR_FORCE`. Por SSH la pantalla inicial es estática. El
diseño sigue el tamaño del terminal: diseño completo desde 100×30, una sola columna desde 80
columnas, un diseño compacto sin el borde exterior desde 60 columnas. En un terminal de menos de
60×16, las pantallas anteriores a la instalación te piden que amplíes la ventana o que uses
`--ui=plain`; durante la instalación se muestra un bloque de estado mínimo. Cambiar el tamaño de la
ventana nunca cancela ni reinicia nada.

**Con un lector de pantalla, usa `--ui=plain`.** La interfaz de terminal vuelve a dibujar toda la
pantalla, algo que los lectores de pantalla no siguen bien, y no está diseñada para ellos.
`--ui=plain`, el valor predeterminado, es la misma instalación como líneas de texto y preguntas
normales, y es permanente: seguirá disponible sea cual sea el valor predeterminado.

En Windows, la interfaz de terminal necesita una consola que procese secuencias de terminal
virtual: Windows Terminal, o el host de consola de las versiones actuales de Windows 10 y 11, con
PowerShell o `cmd`. No necesita Bash, WSL ni otras herramientas. En una consola antigua vuelve a la
salida de texto. En cada salida, incluidos Ctrl+C y los errores, los modos de la consola se dejan
como estaban.

## Código de salida {#exit-status}

El código de salida es el mismo que el de la instalación en texto: 0 si la instalación tuvo éxito, 1
si falló, se rechazó o se detuvo. Salir de una pantalla nunca lo cambia.

## Clúster y después instancia: `dcctl setup local` (Preview) {#setup-local}

`dcctl setup local` ejecuta la instalación del clúster y después el bootstrap de una primera
instancia en una sola sesión guiada. Está en Preview. `dcctl install` y `dcctl bootstrap` no cambian
y siguen siendo la forma de automatizar cada paso.

Tiene dos etapas, cada una con su propia revisión y su propia aprobación:

1. **El clúster.** Las mismas pantallas que `dcctl install local --ui=tui`, desde la bienvenida
   hasta la revisión. **Aprobar e instalar** instala el clúster. Cuando termina, Intro continúa.
2. **La instancia.** Una pantalla de traspaso indica que el clúster está instalado y muestra el
   comando `dcctl bootstrap` que crea una instancia más adelante. **Continuar con la instancia**
   lleva a las opciones de la instancia y del depósito de la clave raíz, y después a la revisión
   propia de la instancia. **Aprobar y crear la instancia** inicia el bootstrap. La frase de paso
   del depósito se pide en pantalla cuando la ejecución llega a su paso de depósito, y nunca se
   muestra. Tras una ejecución correcta llegan el comprobante del depósito, la contraseña de primer
   inicio de sesión de un solo uso y el resumen de la instancia.

Nada de la instancia se ejecuta sin su propia aprobación, y la aprobación del clúster no responde
a nada de lo que pregunte la ejecución de la instancia. Cada pantalla que lleva más allá (el
traspaso y las dos revisiones) no acepta teclas hasta llevar un momento en pantalla, así que las
teclas escritas por adelantado no pueden aprobar nada.

`dcctl setup local` es solo guiado. Necesita que stdin, stdout y stderr sean un terminal
interactivo (no CI, no `TERM=dumb`), y no tiene `--yes`. Sin un terminal no cambia nada y sale con
2, indicando los dos comandos que se deben ejecutar en su lugar. Sus opciones (`--kube-context`,
`--cluster`, `--compact`, `--ha`, `--no-splash`) solo fijan dónde empiezan las pantallas; cada una
se puede cambiar en ellas.

Si sales o la instancia falla después de la etapa del clúster, el clúster sigue instalado. Setup lo
indica e imprime el comando `dcctl bootstrap` exacto que crea la instancia: con las opciones que
elegiste, si ya las elegiste.

| Código de salida | Significado |
| --- | --- |
| 0 | El clúster está instalado y la instancia se creó |
| 1 | La etapa del clúster falló, o se detuvo después de su aprobación, igual que `dcctl install` sale con 1 |
| 2 | No se cambió nada: se detuvo antes de la aprobación del clúster, o se rechazó antes de empezar |
| 3 | El clúster está instalado; no se creó ninguna instancia (se salió antes de la aprobación de la instancia) |
| 4 | El clúster está instalado; la creación de la instancia falló, o se detuvo después de su aprobación |
