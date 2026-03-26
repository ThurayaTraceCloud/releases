# Releases Proxy

Lightweight Go proxy that serves ThurayaTrace agent binaries by redirecting to GitHub Release assets.

## URL Structure

| Path | Behaviour |
|------|-----------|
| `/agent/latest/{filename}` | 302 redirect to the latest GitHub release asset |
| `/agent/{version}/{filename}` | 302 redirect to a specific release asset (e.g. `v1.2.0`) |
| `/agent/latest/version` | Plain-text latest tag (e.g. `v1.2.0`) |
| `/healthz` | Health check (200 OK) |

## Example

```bash
# Download latest agent binary
curl -fLO https://releases.thurayatrace.cloud/agent/latest/thurayatrace-agent-linux-amd64

# Download specific version
curl -fLO https://releases.thurayatrace.cloud/agent/v1.0.0/thurayatrace-agent-linux-amd64

# Check latest version
curl https://releases.thurayatrace.cloud/agent/latest/version
```

## How It Works

1. On startup and every 5 minutes, the proxy queries the GitHub Releases API to cache the latest release tag.
2. Download requests are answered with a 302 redirect to the corresponding GitHub release asset URL.
3. No binaries are stored or proxied through this service -- clients download directly from GitHub.

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `GITHUB_TOKEN` | _(none)_ | GitHub PAT for private repo access / higher rate limits |
| `GITHUB_ORG` | `ThurayaTraceCloud` | GitHub organisation |
| `GITHUB_REPO` | `agent` | GitHub repository name |

## Deployment

The service runs as a single-replica Deployment on the k3s cluster in `thurayatrace-prod`, exposed via Traefik ingress at `releases.thurayatrace.cloud`.

```bash
kubectl apply -f k8s/
```

## CI

Push to `main` builds and pushes `edge` + SHA tags. Pushing a `v*` tag builds and pushes the semver tag.
