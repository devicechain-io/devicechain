---
title: Guía del usuario
---

# Guía del usuario

Esta guía recorre la consola web de DeviceChain, pantalla por pantalla, en el orden en que la
presenta la propia navegación de la consola. Cada capítulo explica para qué sirve una pantalla,
cuándo usarla y qué esperar, y se ilustra con capturas de la consola tal como se distribuye.

Esta es la guía para trabajar **en la consola**. Para las tareas que se hacen mediante la API o
del lado del dispositivo, como conectar un dispositivo o enviar un comando desde código, consulta
las [Guías](/category/guides).

La guía está escrita para dos públicos:

- **Usuarios y administradores de un inquilino**, que trabajan con los dispositivos, reglas,
  alarmas y paneles de un inquilino. La mayoría de los capítulos son para ti.
- **Operadores de la instancia**, que administran toda la instalación: inquilinos, niveles,
  identidades y proveedores de identidad. Esas pantallas se reúnen en su propio capítulo.

¿Es tu primera vez con DeviceChain? Empieza por [Tu primer dispositivo](../quickstart/first-device.md)
para que fluyan los datos y luego vuelve aquí para conocer la consola. Para entender cómo encajan las
piezas, consulta la sección [Conceptos](../concepts/architecture.md).

## Capítulos

Los capítulos se publican a medida que se completan.

| # | Capítulo | Qué cubre |
|---|---|---|
| 1 | [Inicio de sesión](./signing-in.md) | Los pasos de inicio de sesión, el inicio de sesión único, la elección de inquilino, una sesión caducada y el menú de usuario (tema, idioma, perfil). |
| 2 | [La consola de un vistazo](./console-at-a-glance.md) | La página de inicio, la barra lateral, el indicador de inquilino y lo que se ve cuando un área no está disponible. |
| 3 | [Paneles](./dashboards.md) | Plantillas e importación, visualización, edición en el lienzo, widgets y vinculaciones, exportación e historial de versiones. |
| 4 | [Ver paneles en /dash](./viewing-dashboards-in-dash.md) | El visor de paneles independiente: iniciar sesión, cargar un panel mediante un token, el modo quiosco para pantallas murales y mantener la sesión. |
| 5 | [Alarmas](./alarms.md) | La lista de alarmas en vivo, los filtros, reconocer y limpiar, y exportar la lista como CSV. |
| 6 | [Políticas de notificación](./notification-policies.md) | La lista de políticas y cómo darle horas de silencio a una: zona horaria, franjas semanales y una severidad de excepción. |
| 7 | [Dispositivos](./devices.md) | Listar, crear y crear en lote dispositivos, importar dispositivos desde un archivo CSV y exportar la lista como CSV; deshabilitar y habilitar un dispositivo; las pestañas de detalle, incluidas asignación, conectividad y ubicación, eventos, comandos, configuración (con la restauración de una revisión anterior o la copia de la de otro dispositivo), credenciales y sustitución. |
| 8 | [Tipos de dispositivo y perfiles](./device-types-and-profiles.md) | Tipos de dispositivo y perfiles versionados: métricas, comandos, configuración, publicar o revertir una versión, y descargar una versión como Thing Model. |
| 9 | [Métricas calculadas](./calculated-metrics.md) | Métricas calculadas con una fórmula en un perfil de dispositivo: cómo crear una, el lenguaje de fórmulas, qué ocurre cuando una fórmula no produce valor, el relleno del historial y cómo activar la función. |
| 10 | [Reglas de detección](./detection-rules.md) | Crear reglas con un formulario, en el lienzo o en lenguaje natural; vista previa con reproducción; estado de las reglas y flujo en vivo. |
| 11 | [Comandos a una flota](./commanding-a-fleet.md) | Lotes de comandos: crearlos, seguir su avance, rechazos y cancelación. |
| 12 | [Actualizaciones (por aire)](./updates.md) | Artefactos: subir una imagen de actualización, su confianza y sus firmas, y retirarla; registrar y revocar firmantes; y la pestaña Actualizaciones del dispositivo: asignar, cancelar y abandonar una actualización, y qué significa un dispositivo deshabilitado para las actualizaciones. |
| 13 | [Aprovisionamiento y anclas de confianza](./provisioning-and-trust-anchors.md) | Perfiles de aprovisionamiento para flotas que se registran solas, esperar un dispositivo, anclas de confianza, credenciales y perfiles de certificado, y qué funciona hoy con certificados. |
| 14 | [Integraciones de entrada](./inbound-integrations.md) | Recibir datos de dispositivos desde ChirpStack, The Things Stack o un remitente HTTP genérico: crear una integración, conectar el remitente, alias de dispositivo, dispositivos sin registrar y tráfico. |
| 15 | [Conectores](./connectors.md) | Conectores de salida: crearlos, configurarlos y versionarlos. |
| 16 | [Activos, clientes y áreas](./assets-customers-areas.md) | Las familias del registro: listas, jerarquía, esquemas de propiedades y miembros. |
| 17 | [Geocercas](./geofences.md) | Dibujar y editar geocercas, y su historial. |
| 18 | [Grupos, facetas y Explorar](./groups-facets-browse.md) | Claves de faceta, el generador de consultas de Explorar y guardar un resultado como grupo dinámico. |
| 19 | [Miembros, roles y usuarios de cliente](./members-roles-and-customer-users.md) | Invitar a miembros y cambiar su acceso, los roles propios del inquilino y los accesos limitados a tus clientes: qué ven y cómo compartir paneles con ellos. |
| 20 | [Cuentas de servicio](./service-accounts.md) | Identidades de máquina para scripts e integraciones: qué puede hacer una cuenta, crearla, obtener un token, rotar un secreto y deshabilitarla o eliminarla. |
| 21 | [Auditoría](./audit.md) | El diario de auditoría del inquilino, los filtros y la exportación. |
| 22 | [Ajustes del inquilino](./tenant-settings.md) | Marca, mapa e idioma. |
| 23 | [Administración de la instancia](./administering-the-instance.md) | Inquilinos, eliminaciones, niveles, identidades, roles, proveedores de identidad, proveedores de IA, auditoría de la instancia y ajustes. |
