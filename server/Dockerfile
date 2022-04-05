FROM mcr.microsoft.com/dotnet/sdk:6.0.102-cbl-mariner1.0 as build
WORKDIR /src

COPY Tyger.Server/Tyger.Server.csproj Tyger.Server/
RUN dotnet restore Tyger.Server/Tyger.Server.csproj

COPY . .
WORKDIR /src/Tyger.Server
RUN dotnet publish --no-restore -c release -o /app


# final stage/image
FROM mcr.microsoft.com/dotnet/aspnet:6.0.2-cbl-mariner1.0-distroless as runtime
WORKDIR /app
COPY --from=build /app .
USER app:app
ENTRYPOINT ["/app/tyger.server"]
