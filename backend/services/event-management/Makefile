BUILDDIR ?= $(CURDIR)/build

.PHONY: build
build:
	go build -o $(BUILDDIR)/service .

.PHONY: build-protos
build-protos:
	protoc --go_out=. ./proto/*.proto

clean:
	rm -rf $(BUILDDIR)/*
