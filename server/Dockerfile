FROM mcr.microsoft.com/dotnet/sdk:6.0-cbl-mariner1.0 as build
WORKDIR /src

COPY tyger.server/tyger.server.csproj tyger.server/
RUN dotnet restore tyger.server/tyger.server.csproj

COPY . .
WORKDIR /src/tyger.server
RUN dotnet publish --no-restore -c release -o /app


# final stage/image
FROM mcr.microsoft.com/dotnet/aspnet:6.0-cbl-mariner1.0-distroless
WORKDIR /app
COPY --from=build /app .
USER app:app
ENTRYPOINT ["/app/tyger.server"]
