VERSION ?= 0.0.1
BUILDDIR ?= $(CURDIR)/build
FUNCTIONAL_AREA ?= event-sources

.PHONY: build
build:
	go build -o $(BUILDDIR)/service .

.PHONY: build-stripped
build-stripped:
	go build -ldflags '-s -w' -o $(BUILDDIR)/service .

.PHONY: build-protos
build-protos:
	protoc --go_out=. ./proto/*.proto

.PHONY: vendor
vendor:
	go mod vendor

clean:
	rm -rf $(BUILDDIR)/*

# Build a docker image based on the build target
.PHONY: docker-build
docker-build: vendor build-stripped
	docker build -t devicechain-io/${FUNCTIONAL_AREA}:${VERSION} . -f docker/Dockerfile
