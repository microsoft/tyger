FROM mcr.microsoft.com/cbl-mariner/base/core:2.0-nonroot.20240112-amd64 AS worker-waiter
USER root
RUN tdnf install -y kubernetes-client bind-utils
USER nonroot
