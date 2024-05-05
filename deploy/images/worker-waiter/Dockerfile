FROM --platform=$BUILDPLATFORM mcr.microsoft.com/cbl-mariner/base/core:2.0.20240112 as build
ARG TARGETARCH BUILDOS

RUN tdnf install -y ca-certificates

ARG KUBECTL_VERSION=v1.28.3
WORKDIR /opt/download
RUN curl -LO "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/${TARGETARCH}/kubectl" && chmod +x kubectl


FROM mcr.microsoft.com/cbl-mariner/base/core:2.0-nonroot.20240112 AS worker-waiter
USER root
COPY --from=build /opt/download/kubectl /usr/local/bin/kubectl
USER nonroot
