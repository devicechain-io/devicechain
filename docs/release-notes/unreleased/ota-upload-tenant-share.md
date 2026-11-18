---
section: before-upgrade
---

## en

- **One tenant can no longer take every artifact-upload slot of a replica.** A tenant holds at most half of a replica's concurrent artifact uploads (`updates.maxConcurrentUploads`), rounded up and never fewer than one. An upload over its tenant's share is refused before its body is read, with the same `503` and `Retry-After` an upload gets when the replica is full; retry once one of that tenant's uploads finishes. The replica's total is unchanged.

## es

- **Un tenant ya no puede ocupar todas las cargas de artefactos de una réplica.** Un tenant ocupa como máximo la mitad de las cargas simultáneas de artefactos de una réplica (`updates.maxConcurrentUploads`), redondeada hacia arriba y nunca menos de una. Una carga que supera la cuota de su tenant se rechaza antes de leer su cuerpo, con el mismo `503` y `Retry-After` que recibe una carga cuando la réplica está llena; reintenta cuando termine una de las cargas de ese tenant. El total de la réplica no cambia.

## zh-CN

- **单个租户不能再占满一个副本的全部制品上传名额。** 每个租户最多占用副本并发制品上传数（`updates.maxConcurrentUploads`）的一半，向上取整且至少为一个。超出其租户份额的上传会在读取请求体之前被拒绝，返回与副本已满时相同的 `503` 和 `Retry-After`；待该租户的某个上传完成后再重试。副本的总上限不变。
