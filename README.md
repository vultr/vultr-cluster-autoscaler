# Vultr Cluster Autoscaler

Vultr Cluster Autoscaler is a Vultr-owned, Vultr-only distribution of
[Kubernetes Cluster Autoscaler](https://github.com/kubernetes/autoscaler). It scales
autoscaling-enabled node pools in Vultr Kubernetes Engine (VKE) while consuming the
provider-independent autoscaler core as a Go module.

This repository replaces the in-tree `cloudprovider/vultr` implementation. The binary
registers only the Vultr provider, so `--cloud-provider` defaults to `vultr` and can be
omitted.

## Requirements

- Go 1.26 or newer
- A VKE cluster
- A Vultr API token with access to the cluster
- Autoscaling enabled, with minimum and maximum sizes configured, on each VKE node pool
  that Cluster Autoscaler should manage

## Build

```shell
make build
```

The default build creates a Linux binary named `cluster-autoscaler`. To build for the
host platform during development:

```shell
make build GOOS="$(go env GOOS)" GOARCH="$(go env GOARCH)"
```

Run the test suite and compile verification with:

```shell
make verify
```

Build a container image with:

```shell
make image IMAGE=vultr/vultr-cluster-autoscaler TAG=dev
```

## Release

Releases publish Linux amd64 and arm64 archives to GitHub and a multi-architecture
`vultr/vultr-cluster-autoscaler` image to Docker Hub. A release can be started in either
of these ways:

- Merge a commit to `main` with the exact subject `Release vX.Y.Z #patch`, replacing
  `patch` with `minor` or `major` as appropriate.
- Run the `Release` workflow manually from `main` and provide a `vX.Y.Z` tag.

The workflow creates an annotated tag, publishes the GitHub release and checksums, then
publishes both the version tag and `latest` Docker image manifest. Repository secrets
named `DOCKER_USERNAME` and `DOCKER_PASSWORD` must contain Docker Hub credentials with
permission to push the image.

## Configuration

The `--cloud-config` flag must point to a JSON file containing the VKE cluster ID and
Vultr API token:

```json
{
  "cluster_id": "00000000-0000-0000-0000-000000000000",
  "token": "your-vultr-api-token"
}
```

The provider discovers every node pool in the cluster that has Vultr autoscaling
enabled. Node pool minimum and maximum sizes are read from the Vultr API, so `-nodes`
and `--node-group-auto-discovery` are not used.

Example Kubernetes resources are in [`examples`](examples). Update the image and secret
values before applying them:

```shell
kubectl apply -f examples/cluster-autoscaler-secret.yaml
kubectl apply -f examples/cluster-autoscaler-deployment.yaml
```

## Core dependency

The provider implementation and release lifecycle live in this repository. Generic
autoscaling behavior comes from `sigs.k8s.io/cluster-autoscaler`, pinned in `go.mod`.
Kubernetes module replacements are intentionally kept aligned with that core version.
When upgrading core, upgrade the Kubernetes modules and test the provider as one change.
Vultr API operations use the official `github.com/vultr/govultr/v3` client.

The upstream core currently selects registered providers by name. For that reason the
binary still exposes `--cloud-provider`, but Vultr is the only registered value and is
the default.

## Limitations

- Node auto-provisioning is not supported; Cluster Autoscaler does not create or delete
  VKE node pools.
- Scaling from zero requires template node information, which the Vultr provider does
  not currently implement.
- Pricing and GPU type discovery are not implemented.

## License

Apache License 2.0. See [`LICENSE`](LICENSE).
