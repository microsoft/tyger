FROM --platform=$BUILDPLATFORM mcr.microsoft.com/oss/go/microsoft/golang:1.23.1-bullseye as go-build
ARG TARGETARCH BUILDOS

WORKDIR /go/src/app
COPY ./go.mod ./go.sum ./
ARG TYGER_VERSION="a"
RUN --mount=type=cache,target=/root/.cache/go-build go mod download
COPY . .
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${BUILDOS} GOARCH=${TARGETARCH} go build -ldflags="-s -w -X main.version=${TYGER_VERSION}" -v -o /go/bin/dist/${BUILDOS}/${TARGETARCH}/ ./cmd/buffer-sidecar ./cmd/tyger ./cmd/buffer-copier ./cmd/log-reader

FROM mcr.microsoft.com/azurelinux/distroless/minimal:3.0.20241101 as buffer-sidecar
ARG TARGETARCH BUILDOS

COPY --from=go-build /go/bin/dist/${BUILDOS}/${TARGETARCH}/buffer-sidecar /
USER nonroot:nonroot
ENTRYPOINT ["/buffer-sidecar"]

FROM mcr.microsoft.com/azurelinux/distroless/minimal:3.0.20241101 as tyger-cli
ARG TARGETARCH BUILDOS

WORKDIR /app
COPY --from=go-build /go/bin/dist/${BUILDOS}/${TARGETARCH}/tyger .
USER nonroot:nonroot
ENTRYPOINT ["/app/tyger"]

FROM mcr.microsoft.com/azurelinux/distroless/minimal:3.0.20241101 as buffer-copier
ARG TARGETARCH BUILDOS

WORKDIR /app
COPY --from=go-build /go/bin/dist/${BUILDOS}/${TARGETARCH}/buffer-copier .
USER nonroot:nonroot
ENTRYPOINT ["/app/buffer-copier"]


FROM mcr.microsoft.com/azurelinux/distroless/minimal:3.0.20241101 as log-reader
ARG TARGETARCH BUILDOS

WORKDIR /app
COPY --from=go-build /go/bin/dist/${BUILDOS}/${TARGETARCH}/log-reader .
ENTRYPOINT ["/app/log-reader"]
