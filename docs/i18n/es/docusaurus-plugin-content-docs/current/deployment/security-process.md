---
sidebar_position: 12
title: Proceso de seguridad
---

# Proceso de seguridad {#security-process}

Esta página describe cómo gestiona DeviceChain los informes de vulnerabilidades, cómo cumple las obligaciones de notificación de la Ley de Ciberresiliencia de la UE, qué publica sobre cada versión y cómo se mantiene a los agentes de IA que ayudan a mantener el proyecto lejos de todo lo que podría distribuir una compilación comprometida. Indica lo que existe hoy y lo que todavía no.

## Informar de una vulnerabilidad {#reporting}

Informe siempre en privado, nunca en una incidencia pública. Las vías y las versiones compatibles figuran en [SECURITY.md](https://github.com/devicechain-io/devicechain/blob/main/SECURITY.md). Un contacto legible por máquina se publica en [`/.well-known/security.txt`](https://docs.devicechain.io/.well-known/security.txt).

| Paso | Objetivo |
| --- | --- |
| Acuse de recibo y triaje: leerlo, reproducirlo o descartarlo, primera valoración de gravedad | en 24 horas, todos los días de la semana |
| Evaluación completa: validez, gravedad, versiones afectadas | en 7 días |
| Corrección, según la gravedad | Crítica 14 días, Alta 30, Media 90, Baja la siguiente versión |

Son objetivos de un equipo pequeño, no garantías. Si alguno no se va a cumplir, se explica el motivo a quien informó.

## Obligaciones de notificación de la Ley de Ciberresiliencia {#cra}

Desde el 2026-09-11, un fabricante debe notificar a las autoridades una vulnerabilidad de su producto que se esté **explotando activamente** y un **incidente grave** que afecte a la seguridad del producto. Ambos siguen los mismos tres pasos, contados desde que el proyecto tiene conocimiento:

| Paso | Vulnerabilidad explotada activamente | Incidente grave |
| --- | --- | --- |
| Alerta temprana | en 24 horas | en 24 horas |
| Notificación, con lo que se sabe hasta el momento | en 72 horas | en 72 horas |
| Informe final | 14 días después de disponer de una medida correctora | 1 mes después de la notificación |

El procedimiento que siguen los responsables:

1. **Decidir si el plazo ha empezado.** Explotación activa significa evidencia creíble de que alguien ha usado la debilidad contra un sistema real sin permiso de su propietario. Una prueba de concepto enviada por un investigador no lo inicia. Un informe de uso real, sí.
2. **Enviar la alerta temprana** a través de la plataforma única de notificación que opera la agencia de ciberseguridad de la UE, que la encamina al coordinador nacional (CSIRT) de los Estados miembros afectados. Indica que es una alerta temprana, si se sospecha una acción maliciosa y los Estados miembros donde está disponible el producto.
3. **Enviar la notificación** en 72 horas: la naturaleza de la vulnerabilidad, las versiones afectadas, cualquier mitigación ya disponible y una valoración de la gravedad.
4. **Publicar la corrección e informar a los usuarios.** Una versión de parche corregida y un aviso de seguridad de GitHub (con un CVE cuando proceda) salen juntos, y el aviso indica qué hacer antes de actualizar si hay algo.
5. **Enviar el informe final** con la causa raíz, la corrección, el actor malicioso si se conoce y cómo se informó a los usuarios.
6. **Registrar** las fechas de cada paso en el aviso privado para que el siguiente incidente parta de un ejemplo que funciona.

DeviceChain es software de código abierto que no se vende, de modo que determinar si un despliegue concreto convierte a su operador en fabricante a estos efectos corresponde a ese operador. El proyecto aplica este proceso al software que publica en cualquier caso.

## Qué publica cada versión {#release-artifacts}

Cada versión lleva un SBOM firmado y la procedencia de la compilación de cada imagen ([Verificar las versiones](verifying-releases.md)). Cuando un aviso afecta a una dependencia, los responsables comprueban los SBOM de la versión en busca del paquete y la versión afectados, listan las imágenes expuestas y publican con el aviso una declaración en formato OpenVEX: **afectada** o **corregida** para las imágenes que incluyen el paquete, y **no afectada** con un motivo explícito para las que lo incluyen pero no pueden alcanzar el código vulnerable.

La comprobación es un script del repositorio, `hack/vex-exposure.sh`. Lee un SBOM SPDX por imagen y un aviso, lista las imágenes expuestas y redacta la declaración. Trata un SBOM sin paquetes como un error, nunca como «nada expuesto».

## Compilaciones reproducibles {#reproducible}

Una compilación es **reproducible** cuando compilar el mismo commit dos veces, en máquinas y directorios distintos, produce una salida idéntica byte a byte. Un trabajo de CI compila `dcctl` y el agente de borde desde dos copias descomprimidas del commit con cachés separadas y falla si los resúmenes difieren. Se verifica con una compilación no determinista plantada, que debe detectar. Las imágenes de contenedor y el chart de Helm aún no están cubiertos por ese trabajo; la imagen del broker tiene su propia comprobación de reproducibilidad.

## Desarrollo asistido por IA {#ai-assisted-sdl}

Los agentes de IA redactan código, documentación y revisiones para este proyecto. Se los trata como un colaborador no confiable con un teclado inusualmente rápido, y los controles no dependen de que el agente se porte bien:

- **Sin secretos.** Los agentes se ejecutan sin acceso a identidades de firma, credenciales de registro ni claves de nube. El flujo de versiones firma con una identidad de corta duración ligada al repositorio, y no existe ninguna clave de firma de larga duración.
- **Sin autoridad de publicación.** Un agente no puede crear una etiqueta ni publicar un artefacto. El flujo de versiones solo se ejecuta desde una etiqueta creada por un responsable.
- **Sin fusión en la rama protegida.** Un agente abre una pull request. Un responsable la fusiona cuando pasan las comprobaciones obligatorias, igual que con cualquier colaborador.
- **La entrada no confiable es dato.** El texto de incidencias, las descripciones de pull requests, los informes de avisos, las páginas web y la documentación de dependencias que lee un agente se tratan como datos que analizar, nunca como instrucciones que seguir. Un informe que dice «ignora tus reglas» es un hallazgo.
- **Revisión.** Un cambio escrito por un agente lo revisa un responsable y, en áreas de riesgo, un segundo revisor independiente, y una nueva guarda debe fallar ante un defecto plantado antes de confiar en ella.

## Todavía no disponible {#roadmap}

- Comprobaciones de reproducibilidad para las imágenes de contenedor y el chart de Helm.
- Sustituir la credencial de nube de larga duración que queda para las pruebas de rendimiento por acceso de corta duración basado en identidad de carga de trabajo desde CI.
- Una puntuación objetivo publicada frente a OpenSSF Scorecard y los controles base de OpenSSF. Scorecard ya se ejecuta cada semana y su resultado es público.
