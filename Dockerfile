FROM golang:1.17-bullseye as build

WORKDIR /go/src/app

COPY ./go.mod .

RUN --mount=type=cache,target=/root/.cache/go-mod-download go mod download

COPY . .

RUN --mount=type=cache,target=/root/.cache/go-build \
    go build -o /go/bin/app ./cmd/app/ && \
    go build -o /go/bin/testrecon ./test/testrecon/

FROM gcr.io/distroless/base-debian11 as tyger
COPY --from=build /go/bin/app /

USER nonroot:nonroot
ENTRYPOINT ["/app"]

FROM gcr.io/distroless/base-debian11 as testrecon

COPY --from=build /go/bin/testrecon /

USER nonroot:nonroot
ENTRYPOINT ["/testrecon"]
