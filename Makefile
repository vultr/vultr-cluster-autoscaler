GOOS ?= linux
GOARCH ?= $(shell go env GOARCH)
IMAGE ?= vultr/vultr-cluster-autoscaler
TAG ?= dev
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
VERSION_PACKAGE := github.com/vultr/vultr-cluster-autoscaler/version
LDFLAGS := -s -w -X $(VERSION_PACKAGE).ClusterAutoscalerVersion=$(VERSION)

.PHONY: all build test format verify image clean

all: build

build:
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) go build -ldflags="$(LDFLAGS)" -o cluster-autoscaler

test:
	go test ./...

format:
	gofmt -w $$(find . -name '*.go' -not -path './vendor/*')

verify:
	test -z "$$(gofmt -l $$(find . -name '*.go' -not -path './vendor/*'))"
	go test ./...
	CGO_ENABLED=0 go build ./...

image:
	docker build --platform=linux/$(GOARCH) --build-arg TARGETARCH=$(GOARCH) --build-arg VERSION=$(VERSION) -t $(IMAGE):$(TAG) .

clean:
	rm -f cluster-autoscaler cluster-autoscaler-*
