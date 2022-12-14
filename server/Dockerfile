FROM mcr.microsoft.com/dotnet/sdk:7.0.100-cbl-mariner2.0-amd64 as build
WORKDIR /src

COPY Tyger.Server/Tyger.Server.csproj Tyger.Server/
RUN dotnet restore Tyger.Server/Tyger.Server.csproj

COPY . .
WORKDIR /src/Tyger.Server
RUN dotnet publish --no-restore -c release -o /app


# final stage/image
FROM mcr.microsoft.com/dotnet/aspnet:7.0.0-cbl-mariner2.0-distroless-amd64 as runtime
WORKDIR /app
COPY --from=build /app .
USER app:app
ENTRYPOINT ["/app/tyger.server"]
