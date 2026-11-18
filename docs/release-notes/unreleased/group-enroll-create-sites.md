---
section: whats-new
---

## en

- **New entities join matching dynamic groups when they are created.** A device, asset, area or customer created after a dynamic group was put in use by a detection rule or as an access scope is now enrolled at creation when the group's selector matches an entity with no attributes (for example a negated condition such as `!("retired" in attr)`). Members of such an access scope now see devices created after the group was put in use, and group-scoped rules evaluate them. A group put in use before the upgrade is brought up to date within a minute of the first start of `device-management`: every entity its selector matches, including those created before the upgrade, is enrolled, and from then on new entities are enrolled when they are created. The same holds for a group a v1.6 pod puts in use while old and new pods overlap during the rolling upgrade. An entity created through a v1.6 pod in that window is not enrolled at creation.

## es

- **Las entidades nuevas se incorporan a los grupos dinámicos coincidentes al crearse.** Un dispositivo, activo, área o cliente creado después de que un grupo dinámico empezara a usarse en una regla de detección o como ámbito de acceso ahora se incorpora al crearse si el selector del grupo coincide con una entidad sin atributos (por ejemplo, una condición negada como `!("retired" in attr)`). Los miembros de ese ámbito de acceso ahora ven los dispositivos creados después de que el grupo empezara a usarse, y las reglas limitadas al grupo los evalúan. Un grupo que ya se usaba antes de la actualización se pone al día en el minuto siguiente al primer arranque de `device-management`: se incorporan todas las entidades que coinciden con su selector, incluidas las creadas antes de la actualización, y desde entonces las entidades nuevas se incorporan al crearse. Lo mismo vale para un grupo que un pod v1.6 empiece a usar mientras convivan pods antiguos y nuevos durante la actualización gradual. Una entidad creada a través de un pod v1.6 en ese intervalo no se incorpora al crearse.

## zh-CN

- **新实体在创建时加入匹配的动态组。** 动态组被检测规则或访问范围启用后，如果组的选择器匹配没有属性的实体（例如否定条件 `!("retired" in attr)`），此后创建的设备、资产、区域或客户现在会在创建时加入该组。此类访问范围的成员现在可以看到组启用后创建的设备，限定到组的规则也会评估这些设备。升级前已启用的组会在 `device-management` 首次启动后一分钟内得到更新：其选择器匹配的所有实体（包括升级前创建的实体）都会加入，此后新实体在创建时加入。在滚动升级期间新旧 Pod 并存时，由 v1.6 Pod 启用的组同样如此。在此期间通过 v1.6 Pod 创建的实体不会在创建时加入。
