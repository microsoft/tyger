FROM mcr.microsoft.com/oss/go/microsoft/golang:1.20-bullseye as go-build
WORKDIR /go/src/app
COPY ./go.mod .
ARG COMMIT_HASH=""
RUN --mount=type=cache,target=/root/.cache/go-mod-download go mod download
COPY . .
RUN --mount=type=cache,target=/root/.cache/go-go-build \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w -X main.commit=${COMMIT_HASH}" -v -o /go/bin/dist/linux/amd64/buffer-proxy ./cmd/buffer-proxy

FROM go-build as go-build-full
RUN --mount=type=cache,target=/root/.cache/go-go-build \
    CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags="-s -w -X main.commit=${COMMIT_HASH}" -v -o /go/bin/dist/windows/amd64/buffer-proxy.exe ./cmd/buffer-proxy

RUN --mount=type=cache,target=/root/.cache/go-go-build \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w -X main.commit=${COMMIT_HASH}" -v -o /go/bin/dist/linux/amd64/tyger ./cmd/tyger

RUN --mount=type=cache,target=/root/.cache/go-go-build \
    CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags="-s -w -X main.commit=${COMMIT_HASH}" -v -o /go/bin/dist/windows/amd64/tyger.exe ./cmd/tyger

FROM mcr.microsoft.com/cbl-mariner/base/core:2.0 as runtime-builder
# Create a non-root user and group
RUN mkdir -p /staging/etc \
    && tdnf install -y shadow-utils \
    && tdnf clean all \
    && groupadd \
        --system \
        --gid=101 \
        app \
    && adduser \
        --uid 101 \
        --gid app \
        --shell /bin/false \
        --no-create-home \
        --system \
        app \
    # Copy user/group info to staging
    && cp /etc/passwd /staging/etc/passwd \
    && cp /etc/group /staging/etc/group

FROM mcr.microsoft.com/cbl-mariner/distroless/minimal:2.0 as buffer-proxy
COPY --from=runtime-builder /staging/ /
COPY --from=go-build /go/bin/dist/linux/amd64/buffer-proxy /
USER app:app
ENTRYPOINT ["/buffer-proxy"]

# A container image to distribute the tyger and buffer-proxy binaries and not to be executed.
# Indended usage:
# $ img=$(docker create tyger-cli-binaries)
# $ docker cp "${img}:/dist" /tmp/bar
# $ docker rm $img
FROM scratch as tyger-cli-binaries
COPY --from=go-build-full /go/bin/dist/ /dist
# an endpoint is required to be able to call docker create
ENTRYPOINT [ "x" ]
