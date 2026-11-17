---
section: whats-new
---
## en
- **HTTP command polling per device.** Each device can hold at most two waiting command polls per replica (one, plus a reconnect whose earlier connection the server has not yet seen close). Further waiting polls receive HTTP 429 with `Retry-After`, leaving capacity for other devices. Keep one waiting poll per device and retry as directed; `wait=0` requests and the tenant cap are unchanged.

## es
- **Sondeo de comandos HTTP por dispositivo.** Cada dispositivo puede mantener como máximo dos sondeos de comandos en espera por réplica (uno, más una reconexión cuya conexión anterior el servidor aún no ha visto cerrarse). Los sondeos en espera adicionales reciben HTTP 429 con `Retry-After`, dejando capacidad para otros dispositivos. Mantenga un solo sondeo en espera por dispositivo y reintente según se indica; las solicitudes con `wait=0` y el límite del inquilino no cambian.

## zh-CN
- **按设备限制 HTTP 命令轮询。** 每个设备在每个副本上最多可保持两个等待中的命令轮询（一个，外加一个服务器尚未察觉其旧连接已关闭的重连）。更多的等待轮询会收到 HTTP 429 和 `Retry-After`，为其他设备保留容量。请让每个设备只保持一个等待轮询，并按照提示重试；`wait=0` 请求和租户上限保持不变。
