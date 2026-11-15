{/* Copyright The DeviceChain Authors
    SPDX-License-Identifier: Apache-2.0 */}

# Endurecimiento de producción {#production-hardening}

Antes de abrir una instancia a Internet, restrinja los endpoints operativos en el perímetro,
use certificados TLS de confianza y restrinja el acceso al clúster. Esta página describe los
listeners actuales; versiones anteriores pueden exponer otra superficie. Actualice antes de
confiar en la separación del listener interno descrita aquí.

## Puerto público y autenticación {#public-port}

El puerto HTTP del servicio es **8080**. El chart reenvía `/api/<area>/<path>` a `/<path>`
en cada área con API pública. La ruta por prefijo también reenvía endpoints operativos;
no añade autenticación. Estas rutas son accesibles sin un token de DeviceChain:

| Ruta del servicio | Ruta externa | Qué sirve |
| --- | --- | --- |
| `/metrics` | `/api/<area>/metrics` | Métricas de Prometheus, incluidas etiquetas operativas; limite el acceso a clientes de monitorización. |
| `/healthz` | `/api/<area>/healthz` | Estado de actividad; los detalles del fallo quedan en los logs. |
| `/readyz` | `/api/<area>/readyz` | Estado de disponibilidad. |
| `/graphiql` | `/api/<area>/graphiql` | HTML del explorador solo con herramientas de desarrollo habilitadas; deshabilitadas por defecto. |

`/graphql` también es accesible antes de iniciar sesión: la autenticación y autorización dependen
de la operación. El inicio de sesión, el inicio de SSO y el descubrimiento público deben funcionar
sin token; las operaciones protegidas siguen exigiendo credenciales verificadas y permisos.
Bloquear el explorador no bloquea las consultas GraphQL. La introspección del esquema está
deshabilitada por defecto junto con las herramientas de desarrollo.

La emisión de tokens de servicio `/auth/service-token` y las claves de firma `/auth/jwks` están
en el listener interno separado de user-management, no en el puerto 8080. El chart no publica
ninguna ruta a ese listener; el puerto público devuelve 404 para esas rutas. Mantenga su Service
interno. Los perfiles Go están deshabilitados por defecto y usan otro listener de loopback al
habilitarlos; consulte [Perfilar un servicio](./observability.md#profiling).

## Bloquear rutas operativas en el perímetro {#edge-rule}

En un proxy inverso NGINX **delante de** la entrada o Gateway del chart, añada esta ubicación
al bloque HTTPS `server` de la instancia, antes de la ubicación de proxy general. `devicechain_edge`
representa el upstream existente hacia la entrada o Gateway. NGINX compara ubicaciones con la URI
normalizada, sin la cadena de consulta. Esta regla bloquea las rutas y sus descendientes para
todas las áreas, antes de reescribir el prefijo:

```nginx
location ~* ^/api/[^/]+/(metrics|healthz|readyz|graphiql)(/|$) {
    return 403;
}
location / {
    proxy_pass http://devicechain_edge;
}
```

Es una configuración de proxy inverso, no una anotación de ingress ni un filtro portable de Gateway
API. En otro perímetro, aplique la misma denegación a las rutas normalizadas antes de reenviar.
Compruebe que ninguna ubicación regex anterior ni ubicación `^~` la omita. Cierre el acceso público
directo a la entrada/Gateway y los puertos de servicio para evitar eludir la regla.
Las sondas de Kubernetes y el ServiceMonitor de Prometheus deben mantener sus rutas directas dentro
del clúster, sin pasar por esta denegación pública. Consulte [Observabilidad](./observability.md).

Tras recargar la configuración del perímetro, compruebe desde fuera sin credenciales:

```bash
curl -i https://INSTANCE_HOST/api/device-management/metrics
curl -i https://INSTANCE_HOST/api/device-management/healthz
curl -i https://INSTANCE_HOST/api/device-management/readyz
curl -i https://INSTANCE_HOST/api/device-management/graphiql
```

Cada petición debe devolver **403**, también con cadena de consulta o barra final. Repita para cada
área pública habilitada. Confirme que funcionan el inicio de sesión y una petición GraphQL autorizada,
que las sondas siguen sanas y que Prometheus sigue recogiendo métricas. Cualquier respuesta operativa
desde fuera indica que el despliegue de la regla ha fallado.

## Controles del clúster y las credenciales {#controls}

- Use certificados TLS de confianza en el perímetro público y conserve TLS del broker. Consulte
  [Conexión desde fuera](./connecting-from-outside.md).
- Configure [proxies de confianza](./bootstrap.md#trusted-proxies) para la cadena real, de modo que
  los límites por dirección usen la dirección de origen prevista.
- Restrinja el acceso de red a bases de datos, broker, monitorización y puertos internos;
  una regla pública no aísla los pods de otras cargas del clúster.
- Limite los permisos de Kubernetes para leer Secrets, ejecutar en pods o redirigir puertos.
  Mantenga los perfiles deshabilitados fuera de sesiones de diagnóstico controladas.
- Proteja la clave raíz de la instancia y las copias de seguridad, y ensaye la
  [recuperación ante desastres](./disaster-recovery.md). Use roles con privilegios mínimos y rote
  credenciales filtradas. Revise los límites restantes en el [Modelo de seguridad](../concepts/security-model.md).
