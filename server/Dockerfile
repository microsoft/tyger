FROM --platform=$BUILDPLATFORM mcr.microsoft.com/dotnet/sdk:9.0.308-azurelinux3.0 AS build
ARG TARGETARCH

WORKDIR /cp
RUN mkdir -p -m 777 /cp/logs

WORKDIR /dp
RUN mkdir -p -m 777 /dp/data

WORKDIR /static
RUN echo "TARGETARCH is $TARGETARCH"
RUN curl --fail google.com

RUN if [ "$TARGETARCH" = "arm64" ]; then \
        ARCH="aarch64"; \
    else \
        ARCH="$TARGETARCH"; \
    fi && \
    echo "ARCH is $ARCH"; \
    curl --fail -o /static/curl -L https://github.com/moparisthebest/static-curl/releases/download/v8.11.0/curl-${ARCH} && chmod +x /static/curl

WORKDIR /src

COPY server/Directory.Build.targets server/

COPY server/Common/Tyger.Common.csproj server/Common/
COPY server/Common/packages.lock.json server/Common/

COPY server/ControlPlane/Tyger.ControlPlane.csproj server/ControlPlane/
COPY server/ControlPlane/packages.lock.json server/ControlPlane/

COPY server/DataPlane/Tyger.DataPlane.csproj server/DataPlane/
COPY server/DataPlane/packages.lock.json server/DataPlane/

RUN dotnet restore --arch ${TARGETARCH} server/ControlPlane/Tyger.ControlPlane.csproj
RUN dotnet restore --arch ${TARGETARCH} server/DataPlane/Tyger.DataPlane.csproj

COPY . .
WORKDIR /src/server/ControlPlane
RUN dotnet publish --no-restore --arch ${TARGETARCH} -c release -o /control-plane

WORKDIR /src/server/DataPlane
RUN dotnet publish --no-restore --arch ${TARGETARCH} -c release -o /data-plane

FROM mcr.microsoft.com/dotnet/aspnet:9.0.11-azurelinux3.0 AS runtime-prep

FROM mcr.microsoft.com/dotnet/aspnet:9.0.11-azurelinux3.0-distroless AS tyger-server
COPY --from=build /cp /app
WORKDIR /app/bin
COPY --from=runtime-prep /usr/bin/sleep .
COPY --from=build /static/curl .
COPY --from=build /control-plane .
USER app:app
ENTRYPOINT ["/app/bin/tyger-server"]

FROM mcr.microsoft.com/dotnet/aspnet:9.0.11-azurelinux3.0-distroless AS tyger-data-plane-server
COPY --from=build /dp /app
WORKDIR /app/bin
COPY --from=build /static/curl .
COPY --from=build /data-plane .
USER app:app
ENTRYPOINT ["/app/bin/tyger-data-plane-server"]
