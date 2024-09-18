FROM --platform=$BUILDPLATFORM mcr.microsoft.com/oss/go/microsoft/golang:1.23.1-bullseye as go-build
ARG TARGETARCH BUILDOS

WORKDIR /go/src/app
COPY ./go.mod ./go.sum ./
ARG TYGER_VERSION="a"
RUN --mount=type=cache,target=/root/.cache/go-build go mod download
COPY . .
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${BUILDOS} GOARCH=${TARGETARCH} go build -ldflags="-s -w -X main.version=${TYGER_VERSION}" -v -o /go/bin/dist/${BUILDOS}/${TARGETARCH}/ ./cmd/buffer-sidecar ./cmd/tyger ./cmd/buffer-copier

FROM mcr.microsoft.com/cbl-mariner/distroless/minimal:2.0-nonroot.20240112 as buffer-sidecar
ARG TARGETARCH BUILDOS

COPY --from=go-build /go/bin/dist/${BUILDOS}/${TARGETARCH}/buffer-sidecar /
ENTRYPOINT ["/buffer-sidecar"]

FROM mcr.microsoft.com/cbl-mariner/distroless/minimal:2.0-nonroot.20240112 as tyger-cli
ARG TARGETARCH BUILDOS

WORKDIR /app
COPY --from=go-build /go/bin/dist/${BUILDOS}/${TARGETARCH}/tyger .
ENTRYPOINT ["/app/tyger"]

FROM mcr.microsoft.com/cbl-mariner/distroless/minimal:2.0-nonroot.20240112 as buffer-copier
ARG TARGETARCH BUILDOS

WORKDIR /app
COPY --from=go-build /go/bin/dist/${BUILDOS}/${TARGETARCH}/buffer-copier .
ENTRYPOINT ["/app/buffer-copier"]
