BUILDDIR ?= $(CURDIR)/build

.PHONY: build
build:
	go build -o $(BUILDDIR)/dc-simulator .

.PHONY: test
test:
	go test ./...

clean:
	rm -rf $(BUILDDIR)/*
