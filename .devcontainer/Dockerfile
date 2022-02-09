# See here for image contents: https://github.com/microsoft/vscode-dev-containers/tree/v0.205.2/containers/go/.devcontainer/base.Dockerfile

ARG VARIANT="1.17-bullseye"
FROM mcr.microsoft.com/vscode/devcontainers/go:0-${VARIANT}

ARG VSCODE_DEV_CONTAINERS_SCRIPT_LIBRARY_VERSION=v0.215.1

ARG AZCLI_VERSION=2.32.0
RUN script=$(curl -fsSL "https://raw.githubusercontent.com/microsoft/vscode-dev-containers/${VSCODE_DEV_CONTAINERS_SCRIPT_LIBRARY_VERSION}/script-library/azcli-debian.sh") && bash -c "$script" -- "${AZCLI_VERSION}"

ARG KUBECTL_VERSION=1.23.3
ARG HELM_VERSION=3.8.0
RUN script=$(curl -fsSL "https://raw.githubusercontent.com/microsoft/vscode-dev-containers/${VSCODE_DEV_CONTAINERS_SCRIPT_LIBRARY_VERSION}/script-library/kubectl-helm-debian.sh") && bash -c "$script" -- "${KUBECTL_VERSION}" "${HELM_VERSION}"

ARG DOTNET_SDK_VERSION=6.0.102
ARG DOTNET_RUNTIME_VERSION=6.0.2
RUN script=$(curl -fsSL "https://raw.githubusercontent.com/microsoft/vscode-dev-containers/${VSCODE_DEV_CONTAINERS_SCRIPT_LIBRARY_VERSION}/script-library/dotnet-debian.sh") && bash -c "$script" -- "${DOTNET_SDK_VERSION}"

# Download symbols
RUN dotnet tool install dotnet-symbol --tool-path /opt/tools
RUN /opt/tools/dotnet-symbol --symbols /usr/share/dotnet/shared/Microsoft.NETCore.App/${DOTNET_RUNTIME_VERSION}/* \
    && /opt/tools/dotnet-symbol --symbols /usr/share/dotnet/shared/Microsoft.AspNetCore.App/${DOTNET_RUNTIME_VERSION}/*

WORKDIR /opt/temp
COPY install-tools.sh .
RUN ./install-tools.sh

RUN apt-get update && export DEBIAN_FRONTEND=noninteractive \
    && apt-get -y install --no-install-recommends iputils-ping
