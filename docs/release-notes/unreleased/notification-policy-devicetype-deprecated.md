---
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
section: before-upgrade
---

## en

- **Notification-policy `deviceTypeToken` is deprecated in v1.10.** Device-type scoping was never applied. The field remains available on the create input, policy output and search criteria, working exactly as before until it is refused in a later major release, after the deprecation period: non-blank writes are refused, accepted policies read null, and any supplied filter value matches no API-created policy. Leave the field and filter unset for tenant-wide policies. Scoping by anchor or group is planned, with no release version promised.

## es

- **`deviceTypeToken` de las políticas de notificación está obsoleto desde v1.10.** El acotamiento por tipo de dispositivo nunca se aplicó. El campo sigue disponible en la entrada de creación, la salida de la política y los criterios de búsqueda, funcionando exactamente como antes hasta que se rechace en una versión mayor posterior, una vez transcurrido el período de obsolescencia: las escrituras no en blanco se rechazan, las políticas aceptadas se leen con null y cualquier valor del filtro no encuentra ninguna política creada mediante la API. Deja el campo y el filtro sin establecer para las políticas de todo el inquilino. Se prevé acotar por ancla o grupo, sin prometer una versión de lanzamiento.

## zh-CN

- **通知策略的 `deviceTypeToken` 自 v1.10 起弃用。** 设备类型作用域从未生效。该字段在创建输入、策略输出和搜索条件中继续保留，并在之后的某个主版本拒绝它之前完全保持原有行为，且须在弃用期结束之后：非空白值写入被拒绝，已接受的策略读取时该字段为 null，筛选器只要提供了值，就不会匹配任何通过 API 创建的策略。租户级策略请省略该字段和筛选器。计划支持按锚点或组限定作用域，但不承诺发布版本。
