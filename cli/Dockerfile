FROM mcr.microsoft.com/oss/go/microsoft/golang:1.21.3-bullseye as go-build

RUN apt-get update && apt-get install -y --no-install-recommends \
    upx \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /go/src/app
COPY ./go.mod ./go.sum ./
ARG TYGER_VERSION="a"
RUN --mount=type=cache,target=/root/.cache/go-build go mod download
COPY . .
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w -X main.version=${TYGER_VERSION}" -v -o /go/bin/dist/linux/amd64/buffer-sidecar ./cmd/buffer-sidecar \
    && upx /go/bin/dist/linux/amd64/buffer-sidecar

FROM mcr.microsoft.com/cbl-mariner/distroless/minimal:2.0-nonroot.20240112-amd64 as buffer-sidecar
COPY --from=go-build /go/bin/dist/linux/amd64/buffer-sidecar /
ENTRYPOINT ["/buffer-sidecar"]
