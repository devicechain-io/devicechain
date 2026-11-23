---
section: whats-new
---

## en

- **A dead MQTT broker no longer wedges a busy client.** The external MQTT event source, the Sparkplug host and the edge agent's cloud uplink now bound each socket write to the broker at 5 seconds and state their 30 second keep-alive explicitly. Before, a publisher that had filled its socket against a peer that stopped answering could block on that write indefinitely and never notice the dead connection; it now drops the connection and reconnects.

## es

- **Un broker MQTT caído ya no bloquea a un cliente ocupado.** El origen de eventos MQTT externo, el host Sparkplug y el enlace ascendente del agente de borde hacia la nube ahora limitan cada escritura de socket hacia el broker a 5 segundos y declaran de forma explícita su keep-alive de 30 segundos. Antes, un publicador que había llenado su socket frente a un interlocutor que dejó de responder podía quedarse bloqueado indefinidamente en esa escritura sin detectar la conexión muerta; ahora cierra la conexión y se reconecta.

## zh-CN

- **失效的 MQTT 代理不再卡住繁忙的客户端。** 外部 MQTT 事件源、Sparkplug 主机以及边缘代理的云端上行链路现在会将每次向代理的套接字写入限制为 5 秒，并显式声明 30 秒的 keep-alive。此前，如果发布方已向不再响应的对端写满套接字，可能会在该写入上无限期阻塞且察觉不到连接已失效；现在它会断开连接并重新连接。
