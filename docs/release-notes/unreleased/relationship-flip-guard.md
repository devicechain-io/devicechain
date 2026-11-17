---
section: whats-new
---

## en

- **Marking a relationship type tracked is refused while devices created relationships of it.** `updateEntityRelationshipType` refuses to make a type tracked while relationships of that type that devices created themselves exist, because they would become ownership at that moment; the refusal names them. Delete them, then mark the type tracked.

## es

- **Se rechaza marcar un tipo de relación como rastreado mientras existan relaciones de ese tipo creadas por dispositivos.** `updateEntityRelationshipType` rechaza marcar el tipo como rastreado mientras existan relaciones de ese tipo creadas por los propios dispositivos, porque pasarían a otorgar propiedad en ese momento; el rechazo las identifica. Elimínelas y después marque el tipo como rastreado.

## zh-CN

- **如果存在设备自行创建的关系，则拒绝将该关系类型标记为 tracked。** `updateEntityRelationshipType` 会拒绝将类型标记为 tracked，直到该类型中设备自行创建的关系被删除，因为这些关系会在标记时成为所有权关系；拒绝消息会列出它们。请先删除这些关系，再将类型标记为 tracked。
