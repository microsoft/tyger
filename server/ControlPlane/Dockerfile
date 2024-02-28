FROM mcr.microsoft.com/dotnet/sdk:8.0.100-cbl-mariner2.0 as build
WORKDIR /src

COPY server/ControlPlane/Tyger.ControlPlane.csproj server/ControlPlane/
RUN dotnet restore --arch amd64 server/ControlPlane/Tyger.ControlPlane.csproj

COPY . .
WORKDIR /src/server/ControlPlane
RUN dotnet publish --no-restore --arch amd64 -c release -o /app

FROM mcr.microsoft.com/cbl-mariner/base/core:2.0-nonroot.20240112-amd64 as build-amd64

# final stage/image
FROM mcr.microsoft.com/dotnet/aspnet:8.0.0-cbl-mariner2.0-distroless-amd64 as runtime
WORKDIR /app
COPY --from=build-amd64 /usr/bin/sleep .
COPY --from=build /app .
USER app:app
ENTRYPOINT ["/app/tyger-server"]
