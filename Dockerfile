# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.26 AS builder

WORKDIR /workspace
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .

ARG TARGETARCH
ARG VERSION=dev
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH go build \
    -ldflags="-s -w -X github.com/vultr/vultr-cluster-autoscaler/version.ClusterAutoscalerVersion=$VERSION" \
    -o /cluster-autoscaler

FROM gcr.io/distroless/static:nonroot
COPY --from=builder /cluster-autoscaler /cluster-autoscaler
WORKDIR /
ENTRYPOINT ["/cluster-autoscaler"]
