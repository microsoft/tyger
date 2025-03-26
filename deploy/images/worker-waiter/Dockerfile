FROM mcr.microsoft.com/azurelinux/base/core:3.0 AS worker-waiter
USER root
RUN tdnf install -y kubernetes-client && tdnf clean all
USER nonroot
