---
sidebar_position: 16
title: Producto como código
---

# Producto como código {#product-as-code}

Puedes mantener el producto de un tenant en git. `dcctl product export` escribe en un directorio de
archivos los tipos de dispositivo, los perfiles de dispositivo, los paneles, los conectores de salida
y las integraciones de entrada de un tenant. `dcctl product plan` muestra qué cambiaría aplicar esos
archivos, y `dcctl product apply` hace que un tenant los contenga. Úsalo para promover un producto de
un tenant de desarrollo a producción, para revisar un cambio en una pull request o para detectar que
un tenant se ha desviado de lo que está confirmado.

:::caution Vista previa
El árbol de comandos y el formato del paquete (`apiVersion: product.devicechain.io/v1alpha1`) están
en vista previa. Son nuevos, y una versión menor posterior puede cambiar el formato. `dcctl` solo lee
la `apiVersion` que conoce y rechaza cualquier otra, de modo que un paquete nunca se entiende a
medias.
:::

Los comandos hablan con la misma API GraphQL de tenant que la consola, con la identidad con la que
inicias sesión. El servidor autoriza cada llamada, así que necesitas los permisos que la consola
necesitaría para hacer los mismos cambios.

## Exportar, planificar, aplicar {#workflow}

```
dcctl product export ./acme --tenant acme --email you@example.com --password '<password>'
git add acme && git commit -m "Snapshot the acme product"

dcctl product plan  ./acme --tenant acme-prod --email you@example.com --password '<password>'
dcctl product apply ./acme --tenant acme-prod --email you@example.com --password '<password>'
```

`export` escribe en un directorio vacío, inexistente o que ya es un paquete (contiene un
`product.yaml`). Volver a exportar sobre un paquete reescribe sus archivos y elimina los de los
recursos que ya no existen. Exportar dos veces sin cambios entre medias escribe los mismos bytes las
dos veces, así que `git diff` muestra solo los cambios reales. Pasa `--name` para etiquetar el
producto en `product.yaml`.

`plan` lee el tenant e imprime una línea por cambio, sin escribir nada:

```
+ DeviceProfile    thermostat  create
~ Metric           thermostat/m-temp-c  update: unit
- Command          thermostat/c-old  delete (not in the bundle)
^ DeviceProfile    thermostat  publish (the draft changed)
~ Dashboard        overview  update: definition
5 change(s).
```

Una línea empieza por `+` (crear), `~` (actualizar), `-` (eliminar) o `^` (publicar). Frente a un
tenant que ya contiene el paquete, `plan` imprime `No changes`. Añade `--fail-on-changes` para salir
con código distinto de cero cuando haya algún cambio, lo que convierte el plan en una comprobación de
desviación en CI.

`apply` construye el mismo plan, lo imprime y lo ejecuta. Pasa `--yes` para no imprimir el plan
primero. Es un upsert por token, así que ejecutarlo dos veces equivale a ejecutarlo una, y una
ejecución que falla a medias se termina volviendo a ejecutarla.

Pasa `--kinds` para limitar un comando a algunos de `device-types`, `profiles`, `integrations`,
`connectors` y `dashboards`. Sin él, `export` omite los conectores con una advertencia en una
instancia que no ejecuta el área de conectores de salida, y `plan` y `apply` leen solo los tipos que
el paquete contiene.

## Qué contiene un paquete {#layout}

```
product.yaml                 el manifiesto: apiVersion, kind, un name opcional
device-types/<token>.yaml
profiles/<token>.yaml        un perfil con todas sus definiciones
integrations/<token>.yaml
connectors/<token>.yaml
dashboards/<token>.json
```

Cada archivo se llama como el token que contiene, y cada archivo lleva un `kind`. La lectura es
estricta: un campo desconocido, un directorio desconocido, un archivo cuyo nombre no es su token o un
token duplicado es un error que nombra el archivo. Las claves se escriben en orden alfabético, y todo
documento JSON dentro de un archivo se escribe en forma canónica (claves ordenadas, sin espacios
innecesarios), sea cual sea el formato en que lo devolvió el servidor.

### Tipos de dispositivo

```yaml
kind: DeviceType
token: thermo-v1
name: Thermo v1
profile: thermostat          # el token del perfil que adopta el tipo
manufacturer: Acme
model: T1
maintenanceWindow: night     # una referencia, véase "Qué no está en un paquete"
metadata: {sku: T-1}
```

### Perfiles

Un archivo de perfil contiene el perfil y todo lo que se ha creado en él.

```yaml
kind: DeviceProfile
token: thermostat
name: Thermostat
category: hvac
location: {expectedAccuracyMeters: 5.5, expectedUpdateIntervalSeconds: 60}
configurationDeclaration:
  - {key: setpoint, valueType: DOUBLE}
decoder:                      # el documento del decodificador de carga útil, con sus vectores de prueba
  kind: CEL
  envVersion: 1
  input: FRAME
  vectors: [...]
metrics:
  - {token: m-temp-c, key: temp-c, dataType: DOUBLE, unit: Cel, min: -40, max: 85}
calculatedMetrics:
  - {token: m-temp-f, key: temp-f, dataType: DOUBLE, expression: temp-c * 9 / 5 + 32}
commands:
  - token: c-reboot
    key: reboot
    impact: DISRUPTIVE
    parameterSchema: [{name: delay, dataType: INT}]
rules:
  - token: r-overheat
    enabled: true
    definition: {...}         # el documento de la regla de detección
    authoringGraph: {...}     # el lienzo visual con el que se creó, si lo hay
```

Los tokens de definición (`metrics`, `calculatedMetrics`, `commands`, `rules`) son de todo el tenant:
un token nombra una definición esté donde esté. La `key` de una métrica es la clave que usa una
medición, y es única dentro del perfil.

### Paneles

Un archivo de panel es JSON. La definición es el mismo documento opaco que edita la consola.

```json
{
  "kind": "Dashboard",
  "token": "overview",
  "name": "Overview",
  "definition": { "widgets": [], "layout": { "cols": 12 } }
}
```

### Conectores e integraciones

```yaml
kind: Connector
token: audit-sink
type: mqtt
config: {urls: ["tcp://broker.example.com:1883"], topic: alerts, qos: 1}
credential:
  handle: env:DC_CONNECTOR_AUDIT_SINK
```

```yaml
kind: Integration
token: ttn
type: TTS_V3
enabled: true
identity: DEV_EUI
deviceTypes: [thermo-v1]
linkMetrics: true
```

## Los secretos son referencias {#secrets}

Un paquete nunca contiene el valor de un secreto, y esto se impone desde tres lados.

- La plataforma no revela la credencial almacenada de un conector, solo si hay una, y guarda el token
  de portador de una integración únicamente como un resumen. Una exportación no tiene nada que
  escribir.
- En lugar de un valor, un conector nombra su credencial con una **referencia** (handle): `env:NAME`
  significa "la variable de entorno `NAME` cuando se ejecuta `apply`". `export` escribe
  `env:DC_CONNECTOR_<TOKEN>` para un conector que tiene una credencial. Cualquier otro esquema se
  rechaza.
- Todo paquete se analiza en busca de valores con forma de secreto: un valor bajo una clave como
  `password`, `secret` o `apiKey`, un bloque de clave privada, un identificador de clave de acceso en
  la nube, un token web, una credencial de portador o una URL con contraseña. `export` se niega a
  escribir, y `plan` y `apply` se niegan a leer, un paquete con algún hallazgo. El mensaje nombra el
  archivo y la regla y nunca imprime el valor. `--allow-suspected-secrets` en `plan` y `apply`
  desactiva el análisis para un valor que sabes que no es un secreto.

`apply` envía la credencial de un conector solo a un conector que no tiene ninguna. La plataforma no
puede decirte qué hay almacenado, así que no puede saber si la variable ha cambiado; pasa
`--rotate-secrets` para enviarla a todos los conectores cuya variable esté definida. Un conector que
necesita una credencial y cuya variable no está definida bloquea el plan. Quitar `credential` del
archivo de un conector elimina la credencial almacenada.

El token de portador de una integración de entrada lo genera el servidor cuando se crea la
integración. `apply` no lo solicita, de modo que nunca se recibe, se imprime ni se guarda. Rota el
token de la integración en la consola para emitir uno.

## Cómo converge apply {#apply}

Apply se ejecuta en orden de dependencias: conectores, perfiles con sus definiciones, tipos de
dispositivo, integraciones y, por último, paneles. Dentro de un perfil elimina las definiciones que el
paquete ya no enumera, luego actualiza y luego crea, de modo que una clave liberada por una
eliminación pueda reutilizarse.

- **Borradores y luego publicación.** Un perfil, un conector o un panel se converge como borrador y
  después se publica. Apply publica solo cuando algo ha cambiado o el recurso nunca se ha publicado.
  En un perfil también publica cuando las métricas, los comandos, el decodificador o la declaración de
  configuración de la versión activa difieren de los del paquete. De una versión publicada no se
  pueden volver a leer sus reglas ni su declaración de posición, así que un cambio de regla o de
  posición hecho fuera de `apply` no provoca por sí solo una publicación. `--no-publish` converge los
  borradores y se detiene. Las versiones publicadas llevan la etiqueta `dcctl product apply`.
- **Solo lo que nombra el paquete.** Un tipo de dispositivo, perfil, integración, conector o panel
  que está en el tenant y no en el paquete se deja como está, y `plan` lo muestra como nota. La única
  excepción son las definiciones bajo un perfil que el paquete contiene, que convergen a las del
  paquete: `apply` elimina una métrica, un comando o una regla en borrador que no figura en él.
- **Traslados.** Una definición cuyo token se quiere bajo otro perfil se traslada con una
  actualización, no se elimina y se vuelve a crear.
- **Lo que no puede cambiar.** El tipo de una integración, y si una métrica es medida o calculada, se
  fijan al crearlos. Un paquete que discrepa del tenant en cualquiera de los dos es un bloqueo, y
  `apply` rechaza todo el plan antes de escribir nada.

## Ediciones simultáneas {#concurrency}

| Tipo | Protección al aplicar |
| --- | --- |
| Conector | Las actualizaciones y publicaciones están condicionadas al sello de versión leído al hacer el plan. Un conector modificado por otra persona entre medias se rechaza con `modified by another writer`, no se sobrescribe. |
| Panel | Lo mismo: las actualizaciones y publicaciones llevan el sello. |
| Tipo de dispositivo, perfil y sus definiciones, integración | Ninguna. No llevan sello de versión, así que una edición simultánea es de última escritura gana. |

Ejecuta `plan` y `apply` cerca en el tiempo, y vuelve a ejecutar `plan` tras un rechazo.

## Qué no está en un paquete {#limits}

- Las ventanas de mantenimiento, los grupos dinámicos, los clientes, las geovallas, los activos, los
  dispositivos, las credenciales y los canales de notificación no forman parte de un paquete. El
  `maintenanceWindow` de un tipo de dispositivo y el `entityGroupToken` de una regla son referencias
  por token; el tenant al que aplicas ya debe contenerlos, o el servidor rechaza la escritura.
- No se incluye cómo se comparte un panel con clientes y grupos.
- `apply` nunca elimina un tipo de dispositivo, perfil, integración, conector o panel completo.
- `export` escribe borradores. Un borrador que nunca se publicó se exporta igualmente, y `apply` lo
  publica en el otro lado.
