FROM golang:1.17-bullseye as build

RUN apt-get update && apt-get install -y upx

WORKDIR /go/src/app

COPY ./go.mod .

RUN --mount=type=cache,target=/root/.cache/go-mod-download go mod download

COPY . .

RUN --mount=type=cache,target=/root/.cache/go-build \
    go build -ldflags="-s -w" -o /go/bin/server ./cmd/server/ && \
    go build -ldflags="-s -w" -o /go/bin/testrecon ./test/testrecon/

# Whether to compress the runtime binary with upx
ARG COMPRESS

RUN if [ "$COMPRESS" = "1" ]; then upx /go/bin/server; fi

FROM gcr.io/distroless/base-debian11 as tyger-server
COPY --from=build /go/bin/server /

USER nonroot:nonroot
ENTRYPOINT ["/server"]

FROM gcr.io/distroless/base-debian11 as testrecon

COPY --from=build /go/bin/testrecon /

USER nonroot:nonroot
ENTRYPOINT ["/testrecon"]
