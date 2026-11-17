---
section: whats-new
---

## en

Changing a facet's value type is now refused while live SHARED attributes in the same tenant, member family and key hold non-null values tagged with its old type. Clear or migrate those values before changing the declaration. Same-type label and vocabulary edits remain available. No schema migration or configuration change is needed. The check takes effect as device-management pods are replaced; older pods still accept these changes during the rolling upgrade. Pause facet type changes until the rollout finishes.

## es

Se rechaza cambiar el tipo de valor de una faceta mientras existan atributos SHARED activos del mismo tenant, familia de miembros y clave con valores no nulos del tipo anterior. Borre o migre esos valores antes de cambiar la declaración. Se pueden seguir editando la etiqueta y el vocabulario sin cambiar el tipo. No se necesita migración del esquema ni cambio de configuración. La comprobación se activa al reemplazar los pods de device-management; los pods antiguos todavía aceptan estos cambios durante la actualización gradual. Suspenda los cambios de tipo de faceta hasta que termine el despliegue.

## zh-CN

如果同一租户、成员类型和键下的有效 SHARED 属性仍有标记为旧类型的非空值，更改分面的值类型将被拒绝。请先清除或迁移这些值，再更改声明。保持类型不变时，仍可编辑标签和值词汇表。无需数据库结构迁移或配置更改。检查会随着 device-management Pod 的替换而生效；滚动升级期间旧 Pod 仍会接受这些更改。请暂停分面类型更改，直到部署完成。
