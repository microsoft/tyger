FROM --platform=$BUILDPLATFORM mcr.microsoft.com/oss/go/microsoft/golang:1.23.1-bullseye as go-build
ARG TARGETARCH BUILDOS

WORKDIR /go/src/app

COPY ./go.mod ./go.sum ./

RUN --mount=type=cache,target=/root/.cache/go-build go mod download

COPY . .

RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${BUILDOS} GOARCH=${TARGETARCH} go build -tags integrationtest -ldflags="-s -w" -o /go/bin/testconnectivity ./integrationtest/testconnectivity/

FROM mcr.microsoft.com/azurelinux/distroless/minimal:3.0.20241101 as testconnectivity
COPY --from=go-build /go/bin/testconnectivity /
ENTRYPOINT ["/testconnectivity"]
