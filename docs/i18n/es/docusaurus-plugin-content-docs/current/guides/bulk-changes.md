---
title: Cambios masivos en dispositivos
---

# Cambios masivos en dispositivos

**Preview.** Usa `dcctl bulk-change` para planificar, crear, iniciar, consultar, pausar, reanudar, abortar o revertir un cambio `config.set` en varios dispositivos, y para resolver las líneas que quedaron ambiguas. Establece o elimina una clave de configuración declarada con ámbito SHARED.

Los cambios masivos se sirven desde la v1.10, en la API GraphQL de device-management (`createBulkChange`, `startBulkChange`, `bulkChange` y las demás aparecen en la [referencia del esquema GraphQL](/reference/graphql/)) y mediante estos comandos. Ambos son Preview: pueden cambiar en una versión menor. Un servidor anterior a la v1.10 responde **this instance does not serve bulk changes**. Durante una actualización gradual a la v1.10, el esquema descubierto y el pod que responde pueden diferir, y solo los pods actualizados aplican líneas; **still upgrading; retry** indica que debes esperar y volver a intentarlo. Una API no disponible nunca se presenta como un token inexistente.

Un cambio solo se ejecuta cuando la instancia está activada para ello: `dcctl bulk-change readiness` incluye `config.set` entre los tipos ejecutables cuando la base de datos aplica el protocolo de escritura de atributos de dispositivo y tu tenant no tiene filas de atributos huérfanas. Iniciar y reanudar comprueban esos mismos dos hechos.

## Acceso {#access}

Inicia sesión como miembro del tenant con `--tenant`, `--email` y `--password`; usa `--otp` cuando se solicite un segundo factor. Selecciona el host con `--server` y `--tls`, o usa `--instance` para leer el host y la configuración TLS declarados.

Planificar, crear, iniciar, reanudar, revertir y resolver requieren `device:write` y `fleet:operate`. Pausar y abortar requieren `device:write`; las consultas requieren `device:read`. Las identidades de agente o servicio y los miembros con ámbito restringido no pueden usar estas operaciones. El nombre obsoleto `dcctl fleet-op` conserva únicamente `readiness`.

## Planificar y crear {#plan-and-create}

Prepara un archivo UTF-8 con un token de dispositivo por línea no vacía, o repite `--device TOKEN`. Usa una sola fuente de destinos. Se admiten hasta 10.000 tokens y 1 MiB de datos de archivo.

```bash
cat > devices.txt <<'EOF'
meter-001
meter-002
EOF

dcctl bulk-change plan --kind config.set --key mode --value eco --type STRING --devices-file devices.txt --tenant acme --email operator@example.com --password <password>
dcctl bulk-change create --kind config.set --key mode --value eco --type STRING --devices-file devices.txt --tenant acme --email operator@example.com --password <password> -o json
```

El plan no escribe datos y muestra los recuentos admitidos y rechazados, los motivos de rechazo y los resúmenes criptográficos del servidor. Crear fija los destinos en READY y devuelve el contrato y su resumen. Los dispositivos inexistentes se convierten en líneas rechazadas.

Usa `--value V --type T` con STRING, LONG, DOUBLE, BOOLEAN o JSON, o `--unset` sin valor ni tipo. Un valor STRING vacío es válido. Los límites opcionales son `--max-posts-per-second` (10 por defecto, máximo 50), `--failure-threshold` y `--ambiguity-threshold` (recuentos positivos; sin límite si se omiten), y `--settle-deadline` (24h por defecto, entre 60s y 168h; duración o segundos enteros).

El token predeterminado es SHA-256 de `tenant || bytes exactos del archivo || kind || intención canónica`. Los valores repetidos de `--device` usan su matriz JSON ordenada en lugar de los bytes del archivo. Una solicitud idéntica devuelve el cambio original; otra solicitud con el mismo token produce un conflicto. Los finales de línea afectan al token. Usa `--token` para elegirlo y `--name` para un nombre visible; por defecto, el nombre es el token. Cambiar los límites con un token predeterminado puede producir un conflicto: elige otro token para una solicitud nueva.

## Revisar e iniciar {#review-and-start}

Revisa el contrato devuelto al crear y copia su `contractDigest` exacto en start. El cliente nunca calcula el resumen de aprobación.

```bash
dcctl bulk-change start <token> --contract-digest <reviewed-server-digest> --tenant acme --email operator@example.com --password <password>
dcctl bulk-change status <token> --watch --tenant acme --email operator@example.com --password <password>
```

El estado siempre muestra por separado el despacho y las evidencias. Códigos de salida:

| Código | Significado |
| --- | --- |
| 0 | DISPATCHED y evidencias CLOSED |
| 1 | Sigue en curso sin watch, estado desconocido, interrupción o error |
| 3 | PAUSED |
| 4 | ABORTED, incluso si las evidencias siguen OPEN |

DISPATCHED con evidencias OPEN sigue esperando veredictos de los dispositivos. Watch consulta hasta que se cierren las evidencias, se pause o aborte el cambio, o lo interrumpas. Con `-o json`, cada muestra es un objeto JSON en una línea independiente.

## Consultar líneas {#inspect-lines}

El cliente lee todas las páginas por orden de ordinal de entrada. Los filtros se aplican en el servidor. JSON incluye aplicación, resolución, fundamento de las evidencias, revisiones, errores y borrado de identificadores; CSV entrecomilla comas, comillas y saltos de línea.

```bash
dcctl bulk-change lines <token> --posting POSTED --settlement AMBIGUOUS -o csv --tenant acme --email operator@example.com --password <password>
dcctl bulk-change lines <token> --devices-file devices.txt -o json --tenant acme --email operator@example.com --password <password>
```

La opción `--devices-file` une el archivo original por ordinal y añade `inputToken` como procedencia aportada por el usuario, separada del `targetToken` del servidor. Usa el archivo original en el mismo orden. Los duplicados se normalizan igual que al crear. Los identificadores borrados permanecen ocultos. Un resultado vacío puede indicar que las líneas caducaron pero la cabecera sigue existiendo; una cabecera inexistente es un error.

## Pausar, reanudar y abortar {#pause-resume-and-abort}

```bash
dcctl bulk-change pause <token> --tenant acme --email operator@example.com --password <password>
dcctl bulk-change resume <token> --max-posts-per-second 2 --tenant acme --email operator@example.com --password <password>
dcctl bulk-change abort <token> --tenant acme --email operator@example.com --password <password>
```

Pausar detiene las nuevas aplicaciones hasta reanudar. Los límites omitidos al reanudar no cambian. Resume acepta la velocidad, los umbrales y `--contract-digest` opcional; sin este último, solicita una vista previa al servidor y usa su resumen. Esta versión rechaza ampliar el contrato o reanudar con umbrales aún superados.

Abortar cancela permanentemente las líneas pendientes. Las líneas ya aplicadas siguen recibiendo veredictos y las evidencias pueden permanecer OPEN. Un tiempo de espera agotado al obtener la exclusión de control es un error; consulta el estado antes de decidir qué hacer.

## Revertir un cambio masivo {#reverse}

Una reversión es un nuevo cambio masivo que restaura, línea por línea, el valor que cada dispositivo tenía antes del original. Solo se puede revertir un cambio masivo en DISPATCHED o ABORTED, y una reversión no puede revertirse a su vez.

```bash
dcctl bulk-change reverse <token> --token <reversal-token> --tenant acme --email operator@example.com --password <password>
dcctl bulk-change reverse <token> --token <reversal-token> --create -o json --tenant acme --email operator@example.com --password <password>
```

Sin `--create` no se escribe nada: el servidor indica cuántas líneas se revertirían, cuántas cambiaron después de aplicarse el original, cuántos dispositivos fueron borrados, cuántas líneas ya restauró otra reversión, cuántas nunca se aplicaron y los resúmenes. Con `--create` la reversión se crea en READY. Revisa su contrato e iníciala con su `contractDigest`, como cualquier cambio masivo. Repetir `--create` con el mismo `--token` y la misma solicitud devuelve la primera reversión.

`--token` es obligatorio y nombra la reversión; `--name` toma su valor por defecto. `--ordinals 1,4,9` revierte solo esas líneas del original; si se omite, se revierten todas las líneas aplicadas. Las opciones de velocidad, umbrales y `--settle-deadline` funcionan igual que al crear.

Una línea cuyo valor cambió después de aplicarse el original se rechaza con CHANGED_SINCE, de modo que un cambio posterior nunca se sobrescribe por accidente. Para aplicarla de todos modos, añade `--override-changed` (todas las líneas seleccionadas) o `--override-ordinals 4,9` (solo esas líneas). Crear una reversión con una anulación requiere un segundo factor verificado en los últimos cinco minutos; consulta [segundo factor](#second-factor).

## Resolver líneas ambiguas {#resolve}

Una línea pasa a AMBIGUOUS cuando ninguna evidencia del dispositivo la decidió antes de su plazo. Una persona puede registrar un veredicto sobre ella:

```bash
dcctl bulk-change resolve <token> --ordinals 3,7 --verdict confirmed --note "checked on site" --tenant acme --email operator@example.com --password <password>
dcctl bulk-change resolve <token> --all-ambiguous --verdict failed --note "devices offline since the outage" --tenant acme --email operator@example.com --password <password>
```

Selecciona líneas con `--ordinals` o todas las líneas ambiguas con `--all-ambiguous`. `--verdict` es `confirmed` o `failed`, y `--note` (de 1 a 1000 caracteres) explica el motivo. Las líneas que no son ambiguas no cambian. Un veredicto es el juicio de una persona, no una evidencia del dispositivo: si un informe posterior lo contradice, la línea conserva el veredicto y se marca su indicador de evidencia, visible en `lines -o json`. Resolver siempre requiere un segundo factor reciente.

## Segundo factor {#second-factor}

Resolver, y crear una reversión con una anulación, requieren tu propio inicio de sesión con un segundo factor verificado en los últimos cinco minutos. Una cuenta sin segundo factor se rechaza; configura antes una aplicación de autenticación. Las identidades de agente o servicio siempre se rechazan.

dcctl inicia sesión al empezar el comando, por lo que un código pasado con `--otp` suele ser lo bastante reciente. Si el servidor aún pide uno más reciente (por ejemplo, porque el inicio de sesión usó un código de recuperación, que aquí no cuenta), el comando vuelve a iniciar sesión una vez y pide un nuevo código de autenticación en el terminal. Sin terminal, se detiene e indica `--otp`: vuelve a ejecutar el comando con un código nuevo. La solicitud solo se reenvía tras ese rechazo, que no escribe nada.

Consulta la [referencia de comandos](../reference/dcctl.md#dcctl-bulk-change) para ver todas las opciones.
