---
section: whats-new
---

## en

- **Concurrent asset-hierarchy writes can no longer give an asset two parents or form a cycle.** Every way of creating a containment edge (`setAssetParent`, a single or bulk relationship create, and claim redemption) now takes a per-tenant lock before it checks the hierarchy, so concurrent writers are checked one after another. A relationship create that races another writer and would give an asset a second parent is now refused, as it already was when the two did not overlap; `setAssetParent` still replaces the current parent.

## es

- **Las escrituras concurrentes en la jerarquía de activos ya no pueden dar dos padres a un activo ni formar un ciclo.** Todas las formas de crear una arista de contención (`setAssetParent`, la creación individual o masiva de relaciones y el canje de reclamaciones) toman ahora un bloqueo por tenant antes de comprobar la jerarquía, de modo que las escrituras concurrentes se comprueban una tras otra. Una creación de relación que compite con otra escritura y daría a un activo un segundo padre ahora se rechaza, como ya ocurría cuando ambas no se solapaban; `setAssetParent` sigue sustituyendo al padre actual.

## zh-CN

- **并发的资产层级写入不再会让一个资产拥有两个父资产或形成循环。** 所有创建包含关系边的方式（`setAssetParent`、单个或批量创建关系以及认领兑换）现在都会在检查层级之前获取按租户划分的锁，因此并发写入会依次接受检查。与其他写入竞争、并会让资产拥有第二个父资产的关系创建现在会被拒绝，这与两者不重叠时的行为一致；`setAssetParent` 仍会替换当前父资产。
