---
section: whats-new
---

## en

- **Tenant deletion completes with sharded resolved-events.** On an instance with resolved-events in shard groups, deleting a tenant no longer stays blocked on "no DETECT engine answered" with the tenant's token held. event-processing removed the tenant from every group, but the broker refused its answers, because it sent one answer per group over one connection and the broker accepts one per request. It now sends a single answer that covers every group. During a rolling upgrade, a deletion can stay blocked while old and new user-management or event-processing pods overlap, and it resumes on the coordinator's next pass once both are upgraded, then completes after the usual settle and token-hold waits. Instances without sharded resolved-events are unaffected.

## es

- **La eliminación de tenants se completa con resolved-events particionado.** En una instancia con resolved-events en grupos de particionado, eliminar un tenant ya no se queda bloqueado en "no DETECT engine answered" con el token del tenant retenido. event-processing eliminaba el tenant de todos los grupos, pero el broker rechazaba sus respuestas, porque enviaba una respuesta por grupo sobre una sola conexión y el broker acepta una por solicitud. Ahora envía una única respuesta que cubre todos los grupos. Durante una actualización gradual, una eliminación puede seguir bloqueada mientras coexisten pods antiguos y nuevos de user-management o event-processing, y se reanuda en la siguiente pasada del coordinador una vez actualizados ambos; después se completa tras las esperas habituales de estabilización y de retención del token. Las instancias sin resolved-events particionado no se ven afectadas.

## zh-CN

- **对 resolved-events 分片时，租户删除可以完成。** 在 resolved-events 使用分片组的实例上，删除租户不再停留在 "no DETECT engine answered" 并一直保留租户令牌。event-processing 已从每个组中移除该租户，但代理拒绝了它的应答：它通过同一个连接为每个组各发送一个应答，而代理对每个请求只接受一个。现在它只发送一个涵盖所有组的应答。在滚动升级期间，新旧 user-management 或 event-processing Pod 并存时删除可能仍被阻塞，两者都升级后，删除会在协调器的下一轮继续，并在通常的稳定等待和令牌保留等待之后完成。未对 resolved-events 分片的实例不受影响。
