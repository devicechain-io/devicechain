---
section: before-upgrade
---

## en

- **SSO, MFA and step-up limits count an IPv6 /64 as one source.** SSO begin, MFA verification, and step-up requests from IPv6 addresses in the same /64 now share a per-source rate limit, preventing address rotation within that block from draining the shared global allowance. People who sign in from one IPv6 /64, such as one office or carrier network, now share that limit and can be refused sooner. IPv4 limits and all burst sizes and refill intervals are unchanged.

## es

- **Los límites de SSO, MFA y autenticación reforzada cuentan un /64 IPv6 como un solo origen.** Las solicitudes de inicio de SSO, verificación MFA y autenticación reforzada desde direcciones IPv6 del mismo /64 ahora comparten un límite de frecuencia por origen, para evitar que la rotación de direcciones dentro de ese bloque agote la cuota global compartida. Las personas que inician sesión desde un mismo /64 IPv6, como una oficina o una red de operador, comparten ahora ese límite y pueden ser rechazadas antes. Los límites IPv4, los tamaños de ráfaga y los intervalos de reposición no cambian.

## zh-CN

- **SSO、MFA 和加强认证限制将一个 IPv6 /64 视为同一来源。** 来自同一 /64 内 IPv6 地址的 SSO 发起、MFA 验证和加强认证请求现在共享同一个来源速率限制，防止通过在该地址块内轮换地址耗尽共享的全局配额。从同一 IPv6 /64（例如同一办公室或运营商网络）登录的用户现在共享该限制，可能会更早被拒绝。IPv4 限制、所有突发容量和令牌补充间隔均保持不变。
