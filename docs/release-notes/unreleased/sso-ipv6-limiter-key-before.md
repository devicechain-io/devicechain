---
section: before-upgrade
---

## en

- SSO begin, MFA verification, and step-up requests from IPv6 addresses in the same /64 now share a per-source rate limit, preventing address rotation within that block from draining the shared global allowance. IPv4 limits and all burst sizes and refill intervals are unchanged.

## es

- Las solicitudes de inicio de SSO, verificación MFA y autenticación reforzada desde direcciones IPv6 del mismo /64 ahora comparten un límite de frecuencia por origen, para evitar que la rotación de direcciones dentro de ese bloque agote la cuota global compartida. Los límites IPv4, los tamaños de ráfaga y los intervalos de reposición no cambian.

## zh-CN

- 来自同一 /64 内 IPv6 地址的 SSO 发起、MFA 验证和加强认证请求现在共享同一个来源速率限制，防止通过在该地址块内轮换地址耗尽共享的全局配额。IPv4 限制、所有突发容量和令牌补充间隔均保持不变。
