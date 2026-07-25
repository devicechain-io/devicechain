---
sidebar_position: 2
title: Despliegue y operador
---

# Despliegue y operador

DeviceChain se despliega en dos capas declarativas: un **chart de Helm** renderiza las cargas de trabajo de la plataforma, y un **operador** de Kubernetes (construido con controller-runtime) gestiona el ciclo de vida de `DeviceChainInstance`. Ambas son declarativas y compatibles con GitOps. (Los inquilinos no forman parte del trabajo del operador — son registros de base de datos del plano de control, vea abajo.)

:::note Estado
El chart de Helm renderiza las cargas de trabajo y la configuración por servicio hoy. La **agregación de estado** de instancia del operador y la **recarga en caliente de configuración** están en curso. Los overlays de Kustomize por entorno están planificados.
:::

## Desplegando con Helm

El chart en `deploy/helm/devicechain` renderiza un Deployment + Service por **área funcional habilitada**, junto con los ConfigMaps de configuración por servicio y el Secret de configuración de instancia (porta credenciales de persistencia, por lo que es un Secret en lugar de un ConfigMap). Cada pod expone `/healthz` (liveness) y `/readyz` (readiness) para que un servicio que no está listo se mantenga fuera de rotación.

Usted elige qué servicios ejecutar con **ya sea** un perfil nombrado **o** un conjunto explícito:

| Perfil | Áreas funcionales |
|---|---|
| `default` | user-management, device-management, event-sources, event-management, device-state, dashboard-management, command-delivery, notification-management, event-processing — el sistema estándar, y a lo que resuelve un perfil sin establecer |
| `full` | todo lo de `default`, más `ai-inference`, `outbound-connectors`, y `mcp`: las áreas que alcanzan fuera de la instancia, cada una de las cuales conlleva una decisión que tomar deliberadamente (una clave de proveedor de pago, una superficie de salida, una API orientada a agentes) |
| `telemetry` | user-management, device-management, event-sources, event-management, device-state, dashboard-management |
| `ingest-only` | user-management, device-management, event-sources |

```bash
helm install dc deploy/helm/devicechain --set instance.id=devicechain
# run a smaller set of services:
helm install dc deploy/helm/devicechain --set profile=telemetry
```

Para instalar una versión publicada, fije la etiqueta de imagen a una versión — las imágenes publicadas son públicas
en `ghcr.io/devicechain-io`, así que no hace falta construir nada localmente:

```bash
helm install dc deploy/helm/devicechain \
  --set instance.id=devicechain \
  --set image.tag=v1.2.0
```

Vea [Versiones y actualizaciones](./releases-and-upgrades.md) para el modelo de versionado y cómo
`helm upgrade` avanza sin tiempo de inactividad.

`user-management` y `device-management` son el núcleo requerido; `event-management`, `device-state`, y `command-delivery` son opcionales de forma independiente. El chart **falla el renderizado** si una selección omite un servicio del núcleo requerido o una dependencia dura de un servicio habilitado — de modo que una topología rota se detecta en el momento de instalación, no después de que los pods entren en crash-loop. Los valores se validan contra el `values.schema.json` del chart en el momento de aplicación.

## Recursos personalizados

- **`DeviceChainInstance`** (con alcance de clúster) — uno por instalación, declarando la identidad y configuración de la instancia.

Los inquilinos **no** son recursos personalizados — son registros de base de datos del plano de control creados a través de la API de administración de instancia y la consola `/admin`, compartiendo los servicios de la instancia (vea [Multitenencia](../concepts/multi-tenancy.md)).

```bash
kubectl get devicechaininstance      # platform
```

## Separación de responsabilidades

DeviceChain divide deliberadamente cada capa:

| Capa | Herramienta | Responsabilidad |
|---|---|---|
| Infraestructura | **OpenTofu** | NATS, TimescaleDB, namespaces, ingress, TLS |
| Cargas de trabajo | **Chart de Helm** | Deployments, Services, ConfigMaps de configuración por área, y el Secret de configuración de instancia |
| Ciclo de vida | **Operador** | Agregación de estado de `DeviceChainInstance` y recarga en caliente de configuración |
| Configuración de negocio | kubectl / UI | inquilinos y sus ajustes |

OpenTofu se ejecuta una vez en la creación del clúster; el chart renderiza las cargas de trabajo; el operador se ejecuta de forma continua, reconciliando el ciclo de vida. El arranque del clúster nunca vive en el código de la aplicación o del operador — es responsabilidad de la capa de infraestructura. Los módulos de OpenTofu viven en [`deploy/opentofu`](https://github.com/devicechain-io/devicechain/tree/main/deploy/opentofu); aprovisionan el nivel de base de datos con guardas de retención para que sobreviva al desmontaje de la aplicación (vea [Versiones y actualizaciones](./releases-and-upgrades.md#data-durability)).
