# 3-Tier Demo Service Provider

DCM service provider for a 3-tier (web, app, db) demo app. Catalog:
[three_tier_app_demo](https://github.com/dcm-project/catalog-manager/blob/main/api/v1alpha1/servicetypes/three_tier_app_demo/spec.yaml).

---

## Choose one path

| Path | You need | Workloads run |
|------|----------|----------------|
| **A. Mock** | Nothing | In-memory only (`make run`). |
| **B. Podman** | Podman | Containers on your machine; Pet Clinic on **http://localhost:9080**. |
| **C. Kubernetes** | Kind + [api-gateway](https://github.com/dcm-project/api-gateway) Compose + k8s container SP | Pods in Kind; see below. |
| **D. OpenShift** | Same as C, plus kube credentials for `Route` objects | Web URL from **Route** when `SP_WEB_EXPOSURE=openshift` (see Configuration). |

- **Kubernetes path:** set `CONTAINER_SP_URL` to the k8s container SP HTTP base
  URL (no trailing slash). Default **`SP_WEB_EXPOSURE=kubernetes`**: web tier uses an
  external `Service`; **`webEndpoint`** uses the LoadBalancer IP from the k8s SP when available.
- **OpenShift path:** set **`SP_WEB_EXPOSURE=openshift`**. **`SP_OPENSHIFT_ROUTE_NAMESPACE`**
  defaults to **`default`**, matching the k8s container SP’s usual **`NAMESPACE`** default; set it
  to the namespace where that k8s container SP runs if not **`default`**. Use kube credentials
  (**`KUBECONFIG`** / **`SP_OPENSHIFT_KUBECONFIG`** / in-cluster SA) for the **same cluster** as
  **`CONTAINER_SP_URL`** (the k8s container SP). This service needs permission to create and delete
  **`Route`** objects in that namespace. It then creates a Route to **`<name>-web`** and sets
  **`webEndpoint`** to **`https://…`** (edge TLS).
- **Mock / Podman:** leave `CONTAINER_SP_URL` empty; use `DEV_CONTAINER_BACKEND`
  (`mock` or `podman`).

---

## A. Mock (fastest)

```bash
make run
```

```bash
curl -s -X POST http://localhost:8080/api/v1alpha1/three-tier-apps \
  -H "Content-Type: application/json" \
  -d '{"metadata":{"name":"demo"},"spec":{"database":{"engine":"postgres","version":"18"},"app":{"image":"docker.io/springcommunity/spring-framework-petclinic:6.1.2"},"web":{"image":"docker.io/library/nginx:alpine"}}}'
```

---

## B. Podman (full 3-tier app on laptop)

Terminal 1:

```bash
DEV_CONTAINER_BACKEND=podman make run
```

Terminal 2:

```bash
curl -s -X POST http://localhost:8080/api/v1alpha1/three-tier-apps \
  -H "Content-Type: application/json" \
  -d '{"metadata":{"name":"my-petclinic"},"spec":{"database":{"engine":"postgres","version":"18"},"app":{"image":"docker.io/springcommunity/spring-framework-petclinic:6.1.2"},"web":{"image":"docker.io/library/nginx:alpine"}}}'
```

Open **http://localhost:9080** (nginx → app). Delete:

```bash
curl -s -X DELETE http://localhost:8080/api/v1alpha1/three-tier-apps/my-petclinic
```

---

## C. Kubernetes (Kind + api-gateway)

See
[three-tier-app-kind.md](https://github.com/dcm-project/api-gateway/blob/main/docs/three-tier-app-kind.md)
in the **`api-gateway`** repo for the full walkthrough (start stack, create
app, browser access, delete, stop).

---

## Configuration

| Variable | Meaning | Default |
|----------|---------|---------|
| `CONTAINER_SP_URL` | k8s container SP base URL | (empty) |
| `DEV_CONTAINER_BACKEND` | `mock` or `podman` if no `CONTAINER_SP_URL` | `mock` |
| `SP_WEB_EXPOSURE` | `kubernetes` (LB/NodePort via k8s SP) or `openshift` (Route + internal Service) | `kubernetes` |
| `SP_OPENSHIFT_ROUTE_NAMESPACE` | Must match k8s container SP **`NAMESPACE`** (same namespace as Services) | `default` |
| `SP_OPENSHIFT_KUBECONFIG` | Optional kubeconfig; must target the **same cluster** as the k8s container SP | (empty; uses default rules) |
| `SVC_ADDRESS` | Listen address | `:8080` |
| `TIER_STACK_DB_PASSWORD` | DB password | `petclinic` |
| `TIER_STACK_DB_NAME` | DB name | `petclinic` |
| `TIER_STACK_POSTGRES_USER` / `TIER_STACK_MYSQL_USER` | JDBC user | `postgres` / `root` |
| `SP_DCM_REGISTRATION_URL`, `SP_PROVIDER_NAME`, `SP_PROVIDER_ENDPOINT` | Self-registration | (empty) |
| `SP_NATS_URL` | NATS URL for status events to DCM | (empty) |

Optional **`.env`** in the working directory: `cp .env.example .env` (not
committed; replace placeholder passwords).

With **`CONTAINER_SP_URL`**, the SP creates **`<name>-db`**, **`<name>-app`**,
**`<name>-web`** via the k8s SP (**`name`** = **`metadata.name`** on the 3-tier
app). How **`webEndpoint`** is filled depends on **`SP_WEB_EXPOSURE`** (see table above).

For **`openshift`**, the Route must live in the same namespace as the web **Service** the
k8s container SP creates, and the API client must talk to the same cluster—otherwise the
Route would not point at the real **`<name>-web`** Service.


---

## Development

Prerequisites: Go 1.25+, Make, Spectral (for `make check-aep`).

```bash
make build
make run
make test
make fmt vet
make check-aep
```

```bash
make generate-api
make check-generate-api
```

Create waits until all tiers are **RUNNING** (Podman inspect or k8s SP GET).
Optional **`SP_NATS_URL`** sends 3-tier app status to DCM after that.

### Releasing

Images are pushed to `quay.io/dcm-project/three-tier-app-demo-service-provider`.
See [Releasing](https://github.com/dcm-project/shared-workflows#release-flow)
in shared-workflows for the full release process, tag behavior, and version conventions.


## License

Apache License 2.0 — see [LICENSE](LICENSE).
