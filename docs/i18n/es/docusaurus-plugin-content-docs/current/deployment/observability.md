---
sidebar_position: 4
title: Observabilidad y métricas
---

# Observabilidad y métricas

DeviceChain se distribuye con observabilidad integrada, no añadida después: cada servicio
está instrumentado con **métricas de Prometheus** y sondas de salud estándar de Kubernetes, y
`dcctl bootstrap` despliega una pila completa de **Prometheus + Grafana + Alertmanager**
junto con la instancia, de modo que una instalación nueva se puede observar desde su primer minuto,
sin necesidad de armar un proyecto de monitoreo aparte.

:::note Estado
La pila de monitoreo (kube-prometheus-stack a través de `dcctl bootstrap`), el inicio de sesión único (SSO) de Grafana
a través del propio servidor de autorización OAuth 2.1 de la plataforma, y el
panel de operaciones de event-processing están implementados y validados de extremo a extremo.
La amplitud de paneles por servicio (un panel de Grafana curado para cada área funcional)
y el trazado distribuido OTLP son mejoras planeadas a futuro.
:::

## Lo que expone cada servicio

Cada servicio de área funcional se instrumenta a sí mismo con métricas de cliente de Prometheus y
sirve las dos sondas estándar de Kubernetes:

- **`/healthz`** — vitalidad (liveness): ¿el proceso está vivo?
- **`/readyz`** — disponibilidad (readiness): ¿está listo para recibir tráfico? Un servicio que no está
  listo se mantiene fuera de rotación por su Service de Kubernetes (consulte
  [Despliegue y operador](./kubernetes-operator.md)).

Debido a que cada pod habla las mismas convenciones, la pila de monitoreo recolecta métricas de
toda la instancia de manera uniforme; no hay trabajo de integración por servicio.

## La pila de monitoreo

[`dcctl bootstrap`](./bootstrap.md) aprovisiona el monitoreo como uno de sus módulos
de OpenTofu integrados, **activado por defecto**: la misma capa que aprovisiona NATS,
PostgreSQL e ingress también levanta
[kube-prometheus-stack](https://github.com/prometheus-community/helm-charts/tree/main/charts/kube-prometheus-stack)
(Prometheus, Grafana y Alertmanager):

- **Recolección entre espacios de nombres (cross-namespace)** — Prometheus se ejecuta en su propio espacio de nombres y recolecta métricas de
  los servicios de la instancia a través de los espacios de nombres, de modo que una sola pila observa todo el
  despliegue.
- **Los paneles se distribuyen con la plataforma** — los paneles de Grafana viven en el chart de Helm
  (`deploy/helm/devicechain/dashboards/`) y son importados automáticamente por el sidecar de
  paneles de Grafana. Un panel nuevo es un cambio de chart, no una importación manual.
- **Un inicio de sesión de administrador de emergencia (break-glass)** — Grafana conserva una credencial de administrador nativa
  disponible junto con el SSO, de modo que un operador nunca queda bloqueado del acceso a las métricas por una
  interrupción de la autenticación.

## SSO de Grafana

Grafana puede iniciar sesión a los operadores a través del propio sistema de identidad de DeviceChain en lugar de
una cuenta de Grafana separada. Actívelo en el arranque (bootstrap):

```bash
dcctl bootstrap local my-instance --grafana-sso
```

Esto registra a Grafana como un **cliente OAuth confidencial** del servidor de autorización OAuth 2.1 de
`user-management`, el mismo servidor que asegura el
[acceso de IA a través de MCP](../concepts/mcp.md), y configura Grafana para dirigir a los usuarios
a través de él. Iniciar sesión en Grafana es iniciar sesión en DeviceChain.

Dos cosas que hay que saber sobre el modelo:

- **Solo para el nivel de operador.** Las métricas son a nivel de instancia y entre inquilinos, por lo que Grafana
  es una superficie de *operador*, no algo a lo que los usuarios de inquilinos puedan acceder. Los inquilinos ven
  sus propios datos a través de la consola y los paneles, nunca a través de Grafana.
- **Enlazado desde la consola.** La consola de superusuario muestra un enlace de **Métricas**
  que lleva a un operador directamente a Grafana.

## El panel de operaciones de event-processing

El motor DETECT/REACT (consulte [Procesamiento de eventos](../concepts/event-processing.md))
es el componente que un operador más necesita vigilar, y se distribuye con un panel de Grafana
dedicado y alertas. El motor emite métricas orientadas al operador, incluido
un **indicador de retraso del consumidor (consumer-lag gauge)**: cuánto se ha retrasado la detección respecto al
flujo de eventos resueltos, y **recuentos de disparo de reglas**, de modo que "¿el motor de alarmas está al día, y qué
está haciendo?" se puede responder de un vistazo.

## Relacionado

- **[Arrancar una instancia](./bootstrap.md)** — el comando que despliega la
  pila de monitoreo, y sus indicadores (flags).
- **[Despliegue y operador](./kubernetes-operator.md)** — cómo el chart genera
  las cargas de trabajo por servicio con sus sondas de salud.
- **[Acceso de IA (MCP)](../concepts/mcp.md)** — el servidor de autorización OAuth 2.1
  sobre el que se apoya el SSO de Grafana.
