GIT_COMMIT := $(shell git rev-list -1 HEAD)
BUILDDIR ?= $(CURDIR)/build

.PHONY: vendor
vendor:
	go mod vendor

.PHONY: build
build: vendor
	go build -ldflags "-X github.com/devicechain-io/dcctl/cmd.gitCommit=$(GIT_COMMIT)" -o $(BUILDDIR)/dcctl .

clean:
	rm -rf $(BUILDDIR)/*
