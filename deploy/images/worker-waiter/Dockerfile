FROM --platform=$BUILDPLATFORM mcr.microsoft.com/azurelinux/base/core:3.0 AS build
ARG TARGETARCH BUILDOS

RUN tdnf install -y ca-certificates

ARG KUBECTL_VERSION=v1.33.4
WORKDIR /opt/download
RUN curl -LO "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/${TARGETARCH}/kubectl" && chmod +x kubectl


FROM mcr.microsoft.com/azurelinux/base/core:3.0 AS worker-waiter
USER root
COPY --from=build /opt/download/kubectl /usr/local/bin/kubectl
USER nonroot
