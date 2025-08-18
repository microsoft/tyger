FROM --platform=$BUILDPLATFORM mcr.microsoft.com/azurelinux/base/core:3.0 AS go-build
ARG TARGETARCH BUILDOS

RUN tdnf install -y wget tar ca-certificates
RUN wget -O - https://go.dev/dl/go1.25.0.${BUILDOS}-${TARGETARCH}.tar.gz | tar -C /usr/local -xzf -
ENV PATH=${PATH}:/usr/local/go/bin

WORKDIR /go/src/app

COPY ./go.mod ./go.sum ./

RUN --mount=type=cache,target=/root/.cache/go-build go mod download

COPY . .

RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${BUILDOS} GOARCH=${TARGETARCH} go build -tags integrationtest -ldflags="-s -w" -o /go/bin/testconnectivity ./integrationtest/testconnectivity/

FROM mcr.microsoft.com/azurelinux/distroless/minimal:3.0.20241101 as testconnectivity
COPY --from=go-build /go/bin/testconnectivity /
ENTRYPOINT ["/testconnectivity"]
