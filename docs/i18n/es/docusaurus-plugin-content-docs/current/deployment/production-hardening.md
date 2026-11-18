{/* Copyright The DeviceChain Authors
    SPDX-License-Identifier: Apache-2.0 */}

# Endurecimiento de producción {#production-hardening}

Antes de abrir una instancia a Internet, compruebe qué sirve su puerto público, use certificados
TLS de confianza y restrinja el acceso al clúster. Esta página describe los listeners de la
versión actual. Antes de v1.6.0, las métricas y las sondas de salud se servían en el puerto
público de la API y eran accesibles mediante `/api/<area>/...`; actualice antes de confiar en la
separación descrita aquí.

## Puerto público y puerto de operaciones {#public-port}

Los pods y el Service de cada área tienen dos puertos HTTP:

| Puerto | Nombre | Qué sirve | Enrutado desde fuera |
| --- | --- | --- | --- |
| **8080** | `graphql` | `/graphql`, las rutas HTTP propias del área y `/graphiql` con herramientas de desarrollo habilitadas | Sí: el chart reenvía `/api/<area>/<path>` a `/<path>` |
| **8083** | `ops` | `/metrics`, `/healthz` y `/readyz` | No: ninguna ruta del chart apunta a él |

Los endpoints operativos responden sin autenticación; por eso están en un puerto al que no llega
ninguna ruta pública. A través de la ruta pública por prefijo, `/api/<area>/metrics`,
`/api/<area>/healthz` y `/api/<area>/readyz` devuelven **404**: el puerto de la API no sirve
esas rutas. Las sondas de Kubernetes y los ServiceMonitor de Prometheus del chart usan el puerto
`ops` directamente dentro del clúster. Consulte [Observabilidad](./observability.md).

`/graphiql` sirve la página del explorador sin autenticación cuando se habilitan las herramientas
de desarrollo. Están deshabilitadas por defecto, y la introspección del esquema se deshabilita con
ellas.

`/graphql` también es accesible antes de iniciar sesión: la autenticación y autorización dependen
de la operación. El inicio de sesión, el inicio de SSO y el descubrimiento público deben funcionar
sin token; las operaciones protegidas siguen exigiendo credenciales verificadas y permisos.

La emisión de tokens de servicio `/auth/service-token` y las claves de firma `/auth/jwks` están
en el listener interno separado de user-management, no en el puerto 8080. El chart no publica
ninguna ruta a ese listener; el puerto público devuelve 404 para esas rutas. Mantenga su Service
interno. Los perfiles Go están deshabilitados por defecto y usan otro listener de loopback al
habilitarlos; consulte [Perfilar un servicio](./observability.md#profiling).

## Su propia entrada o perímetro {#edge-rule}

Si aporta su propio controlador de entrada, Gateway o proxy inverso, dirija la API solo al puerto
`graphql`, nunca a `ops` ni al listener interno de user-management. Cierre el acceso público
directo a puertos de nodo, balanceadores y puertos de servicio distintos de su perímetro, para que
nadie llegue al puerto `ops` rodeándolo.

Si habilita las herramientas de desarrollo en una instancia accesible desde Internet, bloquee
también el explorador en el perímetro. En un proxy inverso NGINX **delante de** la entrada o
Gateway del chart, añada esta ubicación al bloque HTTPS `server` de la instancia, antes de la
ubicación de proxy general. `devicechain_edge` representa el upstream existente hacia la entrada
o Gateway:

```nginx
location ~* ^/api/[^/]+/graphiql(/|$) {
    return 403;
}
location / {
    proxy_pass http://devicechain_edge;
}
```

Bloquear el explorador no bloquea las consultas GraphQL y, con las herramientas de desarrollo
habilitadas, la introspección del esquema sigue disponible en `/graphql`. Mantenga las
herramientas de desarrollo deshabilitadas en una instancia expuesta a Internet.

Tras una instalación, una actualización o un cambio en el perímetro, compruebe desde fuera sin
credenciales:

```bash
curl -i https://INSTANCE_HOST/api/device-management/metrics
curl -i https://INSTANCE_HOST/api/device-management/healthz
curl -i https://INSTANCE_HOST/api/device-management/readyz
curl -i https://INSTANCE_HOST/api/device-management/graphiql
```

`/metrics`, `/healthz` y `/readyz` deben devolver **404**, o **403** si su propio perímetro
también los bloquea. `/graphiql` debe devolver 404 con las herramientas de desarrollo
deshabilitadas, o 403 con la regla anterior. Repita para cada área pública habilitada. Confirme
que funcionan el inicio de sesión y una petición GraphQL autorizada, que las sondas siguen sanas y
que Prometheus sigue recogiendo métricas. Cualquier respuesta de métricas o de salud desde fuera
indica un perímetro que enruta al puerto `ops`.

## Controles del clúster y las credenciales {#controls}

- Use certificados TLS de confianza en el perímetro público y conserve TLS del broker. Consulte
  [Conexión desde fuera](./connecting-from-outside.md).
- Configure [proxies de confianza](./bootstrap.md#trusted-proxies) para la cadena real, de modo que
  los límites por dirección usen la dirección de origen prevista.
- Restrinja el acceso de red a bases de datos, broker, monitorización y puertos internos de los
  servicios, incluido `ops`; un perímetro público no aísla los pods de otras cargas del clúster.
- Limite los permisos de Kubernetes para leer Secrets, ejecutar en pods o redirigir puertos.
  Mantenga los perfiles deshabilitados fuera de sesiones de diagnóstico controladas.
- Proteja la clave raíz de la instancia y las copias de seguridad, y ensaye la
  [recuperación ante desastres](./disaster-recovery.md). Use roles con privilegios mínimos y rote
  credenciales filtradas. Revise los límites restantes en el [Modelo de seguridad](../concepts/security-model.md).
