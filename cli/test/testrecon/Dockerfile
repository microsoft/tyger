FROM golang:1.17-bullseye as go-build

RUN apt-get update && apt-get install -y upx

WORKDIR /go/src/app

COPY ./go.mod .

RUN --mount=type=cache,target=/root/.cache/go-mod-download go mod download

COPY . .

RUN --mount=type=cache,target=/root/.cache/go-go-build \
    go build -ldflags="-s -w" -o /go/bin/testrecon ./test/testrecon/

FROM gcr.io/distroless/base-debian11 as testrecon

COPY --from=go-build /go/bin/testrecon /

USER nonroot:nonroot
ENTRYPOINT ["/testrecon"]
