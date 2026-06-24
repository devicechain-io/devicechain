FROM golang:1.17-alpine AS build
WORKDIR /build
COPY . .
RUN CGO_ENABLED=0 GOARCH=amd64 GOOS=linux go build -ldflags "-s -w -extldflags '-static'" -o ./service

FROM scratch
COPY --from=build /build/service /service

ENTRYPOINT ["/service"]