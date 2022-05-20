FROM mcr.microsoft.com/oss/go/microsoft/golang:1.17-bullseye as go-build

WORKDIR /go/src/app

COPY ./go.mod .

RUN --mount=type=cache,target=/root/.cache/go-mod-download go mod download

COPY . .

RUN --mount=type=cache,target=/root/.cache/go-go-build \
    go build -ldflags="-s -w" -o /go/bin/testconnectivity ./test/testconnectivity/

FROM cblmariner.azurecr.io/distroless/base:2.0 as testconnectivity

COPY --from=go-build /go/bin/testconnectivity /

ENTRYPOINT ["/testconnectivity"]
