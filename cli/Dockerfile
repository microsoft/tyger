FROM --platform=$BUILDPLATFORM mcr.microsoft.com/azurelinux/base/core:3.0 AS go-build
ARG TARGETARCH BUILDOS BUILDARCH

RUN tdnf install -y wget tar ca-certificates
RUN wget -O - https://go.dev/dl/go1.25.5.${BUILDOS}-${BUILDARCH}.tar.gz | tar -C /usr/local -xzf -
ENV PATH=${PATH}:/usr/local/go/bin

WORKDIR /go/src/app
COPY ./go.mod ./go.sum ./
ARG TYGER_VERSION="a"
RUN --mount=type=cache,target=/root/.cache/go-build go mod download
COPY . .
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${BUILDOS} GOARCH=${TARGETARCH} go build -ldflags="-s -w -X main.version=${TYGER_VERSION}" -v -o /go/bin/dist/${BUILDOS}/${TARGETARCH}/ ./cmd/buffer-sidecar ./cmd/tyger ./cmd/buffer-copier

FROM mcr.microsoft.com/azurelinux/distroless/minimal:3.0 AS buffer-sidecar
ARG TARGETARCH BUILDOS

COPY --from=go-build /go/bin/dist/${BUILDOS}/${TARGETARCH}/buffer-sidecar /
USER nonroot:nonroot
ENTRYPOINT ["/buffer-sidecar"]

FROM mcr.microsoft.com/azurelinux/distroless/minimal:3.0 AS tyger-cli
ARG TARGETARCH BUILDOS

WORKDIR /app
COPY --from=go-build /go/bin/dist/${BUILDOS}/${TARGETARCH}/tyger .
USER nonroot:nonroot
ENTRYPOINT ["/app/tyger"]

FROM mcr.microsoft.com/azurelinux/distroless/minimal:3.0 AS buffer-copier
ARG TARGETARCH BUILDOS

WORKDIR /app
COPY --from=go-build /go/bin/dist/${BUILDOS}/${TARGETARCH}/buffer-copier .
USER nonroot:nonroot
ENTRYPOINT ["/app/buffer-copier"]
