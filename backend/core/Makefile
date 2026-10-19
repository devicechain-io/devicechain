.PHONY: build

build:
		go build -o build/service .

# Regenerates the wire-contract protobuf Go code (resolvedevent, entityevent). go_package is a
# full import path, so the module option makes protoc write each file beside its .proto.
.PHONY: build-protos
build-protos:
	protoc --go_out=. --go_opt=module=github.com/devicechain-io/dc-microservice ./resolvedevent/*.proto ./entityevent/*.proto
