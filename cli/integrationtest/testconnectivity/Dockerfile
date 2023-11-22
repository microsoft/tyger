FROM mcr.microsoft.com/oss/go/microsoft/golang:1.21.3-bullseye as go-build

WORKDIR /go/src/app

COPY ./go.mod ./go.sum ./

RUN --mount=type=cache,target=/root/.cache/go-build go mod download

COPY . .

RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -tags integrationtest -ldflags="-s -w" -o /go/bin/testconnectivity ./integrationtest/testconnectivity/

FROM mcr.microsoft.com/cbl-mariner/distroless/minimal:2.0-nonroot as testconnectivity
COPY --from=go-build /go/bin/testconnectivity /
ENTRYPOINT ["/testconnectivity"]
