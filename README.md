# binpacked

Binpacked is a small Kubernetes dashboard for visualizing node packing, CPU and memory request utilization, and pod placement across node groups.

## Requirements

- Go 1.25+
- Docker with `buildx`
- `kubectl`
- access to a Kubernetes cluster

## Run Locally

Run the app against your current Kubernetes context:

```sh
go run .
```

The app listens on `:8080` by default:

```sh
open http://localhost:8080
```

You can change the listen address if needed:

```sh
go run . -addr :9090
```

## Test

```sh
go test ./...
```

## Build Container

Build the image locally:

```sh
docker buildx build --platform linux/amd64 -t binpacked:local .
```

## Publish Manually To GHCR

Login to GHCR:

```sh
echo "$GITHUB_TOKEN" | docker login ghcr.io -u jcroyoaun --password-stdin
```

Build and push a manual image tag:

```sh
IMAGE_TAG=manual-$(date +%Y%m%d-%H%M%S)
docker buildx build --platform linux/amd64 -t ghcr.io/jcroyoaun/binpacked:$IMAGE_TAG --push .
```

## Deploy

Apply the manifest in this repo:

```sh
kubectl apply -f deploy/quickstart.yaml
kubectl -n binpacked rollout status deployment/binpacked
```

Roll out a specific image tag:

```sh
kubectl -n binpacked set image deployment/binpacked binpacked=ghcr.io/jcroyoaun/binpacked:sha-<12-char-git-sha>
kubectl -n binpacked rollout status deployment/binpacked
```

Roll out a manually published GHCR tag:

```sh
kubectl -n binpacked set image deployment/binpacked binpacked=ghcr.io/jcroyoaun/binpacked:$IMAGE_TAG
kubectl -n binpacked rollout status deployment/binpacked
```

## Verify Deployment

Check the running pods:

```sh
kubectl -n binpacked get pods -o wide
```

Check the exact deployed image:

```sh
kubectl -n binpacked get deployment binpacked -o jsonpath='{.spec.template.spec.containers[0].image}{"\n"}'
```

Port-forward and verify health:

```sh
kubectl -n binpacked port-forward service/binpacked 18081:8080
curl -fsS http://127.0.0.1:18081/api/v1/health
```

## CI / CD

The workflow in `.github/workflows/build-and-push.yml`:

- runs `go test ./...`
- validates the Docker build on pull requests
- publishes `ghcr.io/jcroyoaun/binpacked`
- creates immutable `sha-<12-char-sha>` image tags
- updates `deploy/quickstart.yaml` on pushes to `main` or `master`

Manual workflow dispatch is also supported if you want to publish with a custom tag from GitHub Actions.
