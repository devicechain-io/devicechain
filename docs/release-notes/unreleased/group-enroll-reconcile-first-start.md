---
section: before-upgrade
---

## en

- **Dynamic-group access scopes can widen at the first start.** On its first start after the upgrade, `device-management` adds the existing devices, assets, areas and customers that should already have been members of a dynamic group in use by a detection rule or as an access scope, where the group's selector matches an entity with no attributes (for example a negated condition such as `!("retired" in attr)`). Members of such an access scope may then see devices they could not see before, and group-scoped rules start evaluating them. Review the dynamic groups with such selectors that serve as access scopes before you upgrade.

## es

- **Los ámbitos de acceso de grupos dinámicos pueden ampliarse en el primer arranque.** En su primer arranque tras la actualización, `device-management` incorpora los dispositivos, activos, áreas y clientes existentes que ya deberían haber sido miembros de un grupo dinámico usado en una regla de detección o como ámbito de acceso, cuando el selector del grupo coincide con una entidad sin atributos (por ejemplo, una condición negada como `!("retired" in attr)`). Los miembros de ese ámbito de acceso pueden ver entonces dispositivos que antes no podían ver, y las reglas limitadas al grupo empiezan a evaluarlos. Antes de actualizar, revise los grupos dinámicos con esos selectores que sirven como ámbitos de acceso.

## zh-CN

- **动态组访问范围可能在首次启动时扩大。** 升级后首次启动时，`device-management` 会把本应已属于动态组的现有设备、资产、区域和客户加入该组，前提是该组已被检测规则或访问范围启用，且其选择器匹配没有属性的实体（例如否定条件 `!("retired" in attr)`）。此类访问范围的成员随后可能会看到以前无法看到的设备，限定到组的规则也会开始评估这些设备。请在升级前检查用作访问范围且使用此类选择器的动态组。
