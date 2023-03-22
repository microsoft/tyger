FROM mcr.microsoft.com/dotnet/sdk:7.0.100-cbl-mariner2.0-amd64 as build
WORKDIR /src

COPY . .
WORKDIR /src/tools/Transform-Xml/Transform-Xml
RUN dotnet publish -r win-x64 -p:PublishSingleFile=true --self-contained true -c release -o /dist/windows/amd64/ \
    && dotnet publish -r linux-x64 -p:PublishSingleFile=true --self-contained true -c release -o /dist/linux/amd64/


# A container image to distribute the binaries and not to be executed.
# Indended usage:
# $ img=$(docker create transform-xml-binaries)
# $ docker cp "${img}:/dist" /tmp/bar
# $ docker rm $img
FROM scratch as transform-xml-binaries
COPY --from=build /dist/ /dist
# an endpoint is required to be able to call docker create
ENTRYPOINT [ "x" ]
