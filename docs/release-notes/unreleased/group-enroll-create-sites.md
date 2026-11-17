---
section: whats-new
---

## en

- **New entities join matching dynamic groups when they are created.** A device, asset, area or customer created after a dynamic group was put in use by a detection rule or as an access scope is now enrolled at creation when the group's selector matches an entity with no attributes (for example a negated condition such as `!("retired" in attr)`). Members of such an access scope now see devices created after the group was put in use, and group-scoped rules evaluate them. Entities created before the upgrade are enrolled by a later change. While old and new pods overlap during the rolling upgrade, an entity created through a v1.6 pod is not enrolled until then.

## es

- **Las entidades nuevas se incorporan a los grupos dinámicos coincidentes al crearse.** Un dispositivo, activo, área o cliente creado después de que un grupo dinámico empezara a usarse en una regla de detección o como ámbito de acceso ahora se incorpora al crearse si el selector del grupo coincide con una entidad sin atributos (por ejemplo, una condición negada como `!("retired" in attr)`). Los miembros de ese ámbito de acceso ahora ven los dispositivos creados después de que el grupo empezara a usarse, y las reglas limitadas al grupo los evalúan. Las entidades creadas antes de la actualización se incorporarán mediante un cambio posterior. Mientras convivan pods antiguos y nuevos durante la actualización gradual, una entidad creada a través de un pod v1.6 no se incorporará hasta entonces.

## zh-CN

- **新实体在创建时加入匹配的动态组。** 动态组被检测规则或访问范围启用后，如果组的选择器匹配没有属性的实体（例如否定条件 `!("retired" in attr)`），此后创建的设备、资产、区域或客户现在会在创建时加入该组。此类访问范围的成员现在可以看到组启用后创建的设备，限定到组的规则也会评估这些设备。升级前创建的实体将通过后续变更加入。在滚动升级期间，新旧 Pod 并存时，通过 v1.6 Pod 创建的实体也要等到该后续变更才会加入。
