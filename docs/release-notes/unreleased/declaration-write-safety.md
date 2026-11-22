---
section: before-upgrade
---

## en

- **`dcctl upgrade` now refuses while another dcctl run holds the cluster lock, instead of warning and carrying on.** It takes the lock before its first write and checks the cluster's operator again while it holds it, so a `dcctl install` cannot move the operator underneath it. A run that died gives the lock up on its own within about a minute; a live but stuck one is what `dcctl instances reclaim` is for. Two related changes: `dcctl instances release` refuses when the cluster's operator does not match this dcctl (a cluster with no operator at all is still let through, which is what the command is for), and any dcctl write of an instance declaration refuses one that carries fields this dcctl does not know, rather than erasing them. Use the dcctl that matches the cluster.

## es

- **`dcctl upgrade` ahora se niega mientras otra ejecución de dcctl tiene el bloqueo del clúster, en lugar de avisar y continuar.** Toma el bloqueo antes de su primera escritura y vuelve a comprobar el operador del clúster mientras lo tiene, de modo que un `dcctl install` no puede mover el operador por debajo. Una ejecución que murió suelta el bloqueo sola en un minuto aproximadamente; una viva pero atascada es para lo que sirve `dcctl instances reclaim`. Dos cambios relacionados: `dcctl instances release` se niega cuando el operador del clúster no coincide con este dcctl (un clúster sin ningún operador se sigue dejando pasar, que es para lo que existe el comando), y cualquier escritura de dcctl de una declaración de instancia rechaza una que lleve campos que este dcctl no conoce, en lugar de borrarlos. Usa el dcctl que coincide con el clúster.

## zh-CN

- **当另一个 dcctl 运行持有集群锁时，`dcctl upgrade` 现在会拒绝执行，而不是发出警告后继续。** 它在第一次写入之前获取锁，并在持有锁时再次检查集群的 Operator，因此 `dcctl install` 无法在其运行期间更换 Operator。已终止的运行大约一分钟内会自动释放锁；仍在运行但卡住的运行，请使用 `dcctl instances reclaim`。两项相关变更：当集群的 Operator 与当前 dcctl 不匹配时，`dcctl instances release` 会拒绝执行（完全没有 Operator 的集群仍然放行，这正是该命令的用途）；dcctl 写入实例声明时，如果声明中含有当前 dcctl 不认识的字段，会拒绝写入，而不是将其删除。请使用与集群匹配的 dcctl。
