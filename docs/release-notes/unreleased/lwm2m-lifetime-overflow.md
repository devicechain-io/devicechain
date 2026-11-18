---
section: whats-new
---

## en

- **LwM2M registration lifetimes.** A Register or Update with an extremely large `lt`, past about 292 years, now gets the configured maximum lifetime. Before, the value overflowed, the lifetime fell to the one-minute minimum, and the device was marked disconnected a minute later.

## es

- **Tiempos de vida de los registros LwM2M.** Un Register o Update con un `lt` extremadamente grande, de más de unos 292 años, recibe ahora el tiempo de vida máximo configurado. Antes, el valor se desbordaba, el tiempo de vida caía al mínimo de un minuto y el dispositivo se marcaba como desconectado un minuto después.

## zh-CN

- **LwM2M 注册有效期。** 当 Register 或 Update 携带极大的 `lt`（超过约 292 年）时，现在会获得配置的最大有效期。此前该值会溢出，有效期降到一分钟的下限，设备在一分钟后即被标记为已断开。
