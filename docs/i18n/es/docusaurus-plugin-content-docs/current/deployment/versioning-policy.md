---
sidebar_position: 4.5
title: Política de versionado y actualización
---

# Política de versionado y actualización {#versioning-and-upgrade-policy}

A partir de la **v1.0.0**, las versiones de DeviceChain siguen el [versionado semántico](https://semver.org). Esta página
explica qué promete cada parte del número, qué interfaces cubre la promesa, cómo se pasa de una versión a la siguiente,
cuánto tiempo está obsoleta una funcionalidad antes de eliminarse y cómo se anuncia un cambio disruptivo.

Antes de la v1.0.0 nada de esto se aplica. Consulta [Estabilidad previa a 1.0](./releases-and-upgrades.md#pre-10-stability)
para saber qué puede cambiar una versión previa a 1.0.

## Qué significa un número de versión {#version-numbers}

Una versión es una única etiqueta, `vMAYOR.MENOR.PARCHE`. Las imágenes de los servicios, el chart de Helm, el operador y
`dcctl` se publican con ese mismo número, de modo que un despliegue es un único número coherente. Consulta
[Modelo de versionado](./releases-and-upgrades.md#modelo-de-versionado).

| Parte | Qué puede contener | Qué no contendrá |
| --- | --- | --- |
| **Parche** (`1.4.1`) | Correcciones de errores, correcciones de seguridad y mejoras de rendimiento. | Un cambio en una interfaz cubierta, ni una eliminación. |
| **Menor** (`1.5.0`) | Funcionalidades nuevas, campos y operaciones nuevos, ajustes opcionales nuevos, migraciones de base de datos nuevas y obsolescencias. | La eliminación de algo cubierto más abajo, ni un cambio en lo que significa una llamada existente. |
| **Mayor** (`2.0.0`) | Eliminaciones y cambios de significado, incluidas las cosas que una versión menor declaró obsoletas. | Entre versiones mayores no se promete nada más allá de lo que indiquen sus notas de versión y su sección de actualización. |

## Qué cubre la promesa {#compatibility-surfaces}

La promesa no tiene la misma fuerza para todas las partes de la plataforma. Algunas interfaces son aquello contra lo que
se escriben tus dispositivos, tu código y tus scripts, y solo cambian en una versión mayor. Otras son superficies de
ajuste y de operación, y pueden cambiar en una versión menor siempre que las notas de versión lo indiquen.

### Cubiertas: solo cambian de forma incompatible en una versión mayor {#covered-interfaces}

| Interfaz | Qué se cubre |
| --- | --- |
| **Protocolo de dispositivo** | Las cargas útiles MQTT y HTTP que un dispositivo envía y recibe, la disposición de temas y asuntos, y los tipos de credencial. Un dispositivo que funciona con `1.x` sigue funcionando con cualquier `1.y` posterior. |
| **API GraphQL** | Cada campo, argumento, tipo y operación de los esquemas publicados. Una versión menor añade al esquema; no elimina ni renombra nada, no cambia un tipo, no vuelve obligatorio un argumento opcional ni cambia lo que devuelve un campo existente. |
| **Paquetes npm** | Las exportaciones públicas de `@devicechain/client`, `@devicechain/dashboards`, `@devicechain/widgets`, `@devicechain/widget-sdk` y `@devicechain/brand`. El SDK de widgets tiene además su propio `sdkApiVersion`, que decide si un widget guardado se sigue mostrando; consulta [Versiones de widgets y compatibilidad](../guides/widget-versions-and-compatibility.md). |
| **SDK de C#** | La API pública del paquete NuGet `DeviceChain.Sdk`, sobre el que se construye el paquete de Unity. |
| **`dcctl`** | Sus comandos, subcomandos e indicadores. Una versión menor no elimina un indicador ni le cambia el significado. |

Dos reglas se aplican a lo que una versión menor puede añadir, así que escribe tus clientes para tolerarlo:

- **Campos y tipos nuevos** aparecen en las respuestas de GraphQL. Ignore los que no conozca.
- **Valores nuevos** pueden aparecer en una enumeración. Trata un valor desconocido como "algo que aún no gestiono",
  no como un error.

### No cubiertas: se describen en las notas de versión {#best-effort-interfaces}

Estas pueden cambiar en una versión menor. Cuando lo hacen, las notas de versión y la sección de esa versión en
[Versiones y actualizaciones](./releases-and-upgrades.md) lo indican y explican qué debe hacer:

- Los valores del chart de Helm y el documento de configuración de la instancia
- Los nombres de métricas y de alertas
- El catálogo de herramientas MCP (nombres de las herramientas, entradas y forma de los resultados)

### Vista previa: puede cambiar en una versión menor, durante un tiempo limitado {#preview-interfaces}

Una superficie nueva puede pasar su primera versión menor en un nivel de **vista previa** antes de quedar
cubierta por la promesa anterior. Una superficie en vista previa lo dice donde se lee sobre ella: en su
página de referencia o de guía y, para cada operación, campo, argumento, tipo y valor de enumeración
de GraphQL de esa superficie, en su descripción. Mientras una superficie
está en vista previa puede cambiar de forma incompatible en una versión menor; cuando lo hace, la sección
de esa versión en [Versiones y actualizaciones](./releases-and-upgrades.md) dice qué cambió y qué hacer.
Una superficie permanece en vista previa como máximo durante dos versiones menores; al terminar la segunda
se promueve y queda cubierta como todo lo demás. Un cambio incompatible en una superficie en vista previa
no queda exento de las comprobaciones de compatibilidad del proyecto: se declara allí como una ruptura
intencionada, igual que cualquier otra, de modo que ningún cambio en vista previa se publica sin que se
note. Un formato de archivo en vista previa lleva una versión, como `v1alpha1`, y una herramienta que no
conoce la versión rechaza el archivo en lugar de adivinar.

Hoy el nivel de vista previa contiene tres superficies. Las pruebas de reglas: la operación `evaluateRules`
de la API de event-processing, los formatos de archivo de escenario y de prueba, y el comando `dcctl test`
que los lee (consulta [Probar tus reglas](../guides/testing-your-rules.md)). El producto como código:
`dcctl product export`, `plan` y `apply`, y su formato de paquete (consulta [Producto como código](../guides/product-as-code.md)).
Y las acciones de agentes: los alcances OAuth `act:*` y sus permisos de propuesta derivados;
las operaciones de la política de agentes (`agentPolicy`, `agentPolicyVersions`, `saveAgentPolicyDraft`,
`publishAgentPolicy`, `rollbackAgentPolicy`, `setAgentActuation`) y su documento (formato `v1alpha1`);
las consultas `agentActuationDecision` y `agentExecutionDecision`; los controles del operador por
inquilino (`tenantAgentActuation`, `setTenantAgentActuationSuspended`); la creación de cuentas de
servicio de agentes (`ServiceAccountCreateRequest.agent`) y los permisos de sus tokens descritos en
`ServiceAccount.roles`; y las operaciones de propuestas de agentes (`proposeAlarmAction`,
`approveAgentProposal`, `rejectAgentProposal`, `agentProposals`, `agentProposal`) (consulta
[Acciones de agentes](../concepts/mcp.md#agent-actions)). Las superficies GraphQL en vista previa
incluyen sus tipos de entrada y resultado exclusivos, campos, argumentos y valores de enumeración.
El campo `ServiceAccount.agent` no está en vista previa: está cubierto desde la versión que lo añade.
Los roles de las cuentas de servicio ordinarias siguen cubiertos. Todavía no se ofrece ninguna
herramienta MCP de acción; las herramientas MCP de lectura siguen en el catálogo no cubierto anterior.

## Rutas de actualización admitidas {#upgrade-paths}

- **Dentro de una versión menor** (`1.4.0` a `1.4.3`) y **a la siguiente versión menor** (`1.4.x` a `1.5.0`) son las rutas admitidas.
  Se ejecutan con `dcctl install` y después `dcctl upgrade`, como se describe en
  [Actualizaciones progresivas](./releases-and-upgrades.md#rolling-upgrades).
- **Saltarse una versión menor** no es una ruta probada. Para ir de `1.4.x` a `1.7.0`, actualiza primero a través de `1.5.x`
  y `1.6.x`. La sección de actualización de una versión indica cuándo exige antes una versión concreta, como hace hoy la sección
  del [formato de eventos en el cable](./releases-and-upgrades.md#event-wire-format).
- **Las migraciones de base de datos siempre actualizan.** A partir de la v1.0.0 nunca se recrea una instancia para adoptar una
  versión nueva. Un cambio de esquema es una migración que se añade a la cadena del área, nunca una edición de una anterior, y
  toda migración puede volver a ejecutarse tras un fallo. Antes de la v1.0.0, a algunas versiones no se podía actualizar en
  absoluto; eso termina con la v1.0.0.
- **Las actualizaciones se comprueban con datos reales.** La actualización de cada versión se ejerce sembrando una instancia en una versión
  anterior, actualizándola y leyendo de vuelta todas las filas. El flujo de publicación ejecuta esta comprobación desde la versión
  estable anterior. Si falla, la versión se detiene: no se publican para ella la versión de GitHub, los binarios de `dcctl` ni los
  paquetes de los SDK. Sus imágenes de contenedor y su chart pueden estar ya en el registro, pero no son una versión.
- **No hay descenso de versión.** Una versión nueva puede migrar la base de datos de formas que una versión anterior no puede leer.
  Si una actualización sale mal, avance a una versión corregida o restaure desde una copia de seguridad con el procedimiento de
  [recuperación ante desastres](./disaster-recovery.md). No usa `helm rollback` para volver atrás.

Lee la sección de actualización de la versión a la que vas a pasar antes de ejecutar la actualización. Indica qué comprueba la
actualización, qué rechaza y qué debe hacer antes.

## Obsolescencia {#deprecation}

Antes de que algo cubierto arriba se elimine en una versión mayor, una versión menor lo declara obsoleto.

- **Una obsolescencia dura al menos dos versiones menores** antes de la eliminación, y la eliminación solo ocurre en una versión mayor.
  Algo declarado obsoleto en la `1.5.0` no se elimina antes de la `2.0.0`, ni antes de que se haya publicado la `1.7.0`.
- **Un campo o argumento de GraphQL obsoleto** se marca con la directiva `@deprecated` y un motivo que nombra su
  sustituto. El motivo es visible en el esquema, en la introspección y en la
  [referencia de la API GraphQL](../reference/graphql-api.md).
- **Un indicador, exportación de paquete o ajuste obsoleto** se documenta como obsoleto, con su sustituto,
  en las notas de la versión que lo declara obsoleto.
- **Lo obsoleto sigue funcionando** exactamente igual durante toda la ventana.

Un problema de seguridad es la única excepción. Una corrección que no puede hacerse de forma compatible se publica en cuanto
está lista, en las primeras líneas de las notas de versión, con los pasos para pasar al sustituto.

## Ventana de soporte {#support-window}

| Versión | Qué recibe |
| --- | --- |
| La **última versión menor** | Correcciones de errores y correcciones de seguridad de cualquier gravedad |
| La **versión menor anterior** | Correcciones de seguridad de gravedad alta o crítica, durante 90 días después de publicarse la siguiente versión menor |
| Cualquier versión más antigua | Nada. Actualiza a una versión admitida |

Las correcciones se publican como versiones de parche de cada versión menor admitida. Manténgase a una versión menor de la
última; actualizar a la siguiente versión menor es la ruta admitida, de modo que mantenerse al día nunca supone más de un paso
cada vez. La misma ventana figura en la [política de seguridad](https://github.com/devicechain-io/devicechain/blob/main/SECURITY.md),
junto con cómo informar de una vulnerabilidad y en qué plazo se busca publicar una corrección.

## Cómo se anuncian los cambios disruptivos {#announcing-breaking-changes}

- **Notas de versión.** Cada versión tiene destacados escritos a mano al principio de sus notas en la página de versiones de
  GitHub. Una obsolescencia, un paso obligatorio antes de actualizar o una actualización rechazada se indican ahí primero,
  antes de la lista de cambios.
- **La sección de actualización.** Cada versión tiene una sección en [Versiones y actualizaciones](./releases-and-upgrades.md)
  con el procedimiento, las comprobaciones que realiza y lo que debe cambiar.
- **El esquema.** Un campo de GraphQL obsoleto se marca en el esquema publicado, de modo que una herramienta que lee el esquema
  lo ve antes de que una persona lea las notas.
- **Candidatas a versión.** Una candidata (`vX.Y.Z-rc.N`) es una compilación en la que puede probar una actualización antes de la versión estable.
  Nunca mueve la etiqueta de imagen `:latest`.

Como las notas son el lugar donde se anuncia un cambio, léalas en cada versión que instales, incluidos los parches.
