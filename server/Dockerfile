FROM mcr.microsoft.com/dotnet/sdk:8.0.100-cbl-mariner2.0 as build

WORKDIR /src

COPY server/Directory.Build.targets server/

COPY server/Common/Tyger.Common.csproj server/Common/
COPY server/Common/packages.lock.json server/Common/

COPY server/ControlPlane/Tyger.ControlPlane.csproj server/ControlPlane/
COPY server/ControlPlane/packages.lock.json server/ControlPlane/

COPY server/DataPlane/Tyger.DataPlane.csproj server/DataPlane/
COPY server/DataPlane/packages.lock.json server/DataPlane/

RUN dotnet restore --arch amd64 server/ControlPlane/Tyger.ControlPlane.csproj
RUN dotnet restore --arch amd64 server/DataPlane/Tyger.DataPlane.csproj

COPY . .
WORKDIR /src/server/ControlPlane
RUN dotnet publish --no-restore --arch amd64 -c release -o /control-plane

WORKDIR /src/server/DataPlane
RUN dotnet publish --no-restore --arch amd64 -c release -o /data-plane

FROM mcr.microsoft.com/cbl-mariner/base/core:2.0-nonroot.20240112-amd64 as build-amd64


FROM mcr.microsoft.com/dotnet/sdk:8.0.100-cbl-mariner2.0 as runtime-prep

WORKDIR /cp
RUN mkdir -p -m 777 /cp/logs

WORKDIR /static
RUN curl --fail -o /static/curl -L https://github.com/moparisthebest/static-curl/releases/download/v8.5.0/curl-amd64 && chmod +x /static/curl

WORKDIR /dp
RUN mkdir -p -m 777 /dp/data


FROM mcr.microsoft.com/dotnet/aspnet:8.0.0-cbl-mariner2.0-distroless-amd64 as control-plane
COPY --from=runtime-prep /cp /app
WORKDIR /app/bin
COPY --from=build-amd64 /usr/bin/sleep .
COPY --from=build /control-plane .
USER app:app
ENTRYPOINT ["/app/bin/tyger-server"]

FROM mcr.microsoft.com/dotnet/aspnet:8.0.0-cbl-mariner2.0-distroless-amd64 as data-plane
COPY --from=runtime-prep /dp /app
WORKDIR /app/bin
COPY --from=runtime-prep /static/curl .
COPY --from=build /data-plane .
USER app:app
ENTRYPOINT ["/app/bin/tyger-data-plane-server"]
