FROM mcr.microsoft.com/vscode/devcontainers/base:0.201.8-focal AS devcontainerbase

# Install needed packages and setup non-root user.
ARG USERNAME="vscode"
ARG USER_UID=1000
ARG USER_GID=$USER_UID
ARG CONDA_GID=900
ARG HOME=/home/$USERNAME
ARG CONDA_ENVIRONMENT_NAME=tyger

# The version of conda to use
ARG CONDA_VERSION=4.11.0

# Based on https://github.com/ContinuumIO/docker-images/blob/master/miniconda3/debian/Dockerfile.
RUN wget --quiet https://repo.anaconda.com/miniconda/Miniconda3-latest-Linux-x86_64.sh -O miniconda.sh \
    && mkdir -p /opt \
    && sh miniconda.sh -b -p /opt/conda \
    && ln -s /opt/conda/etc/profile.d/conda.sh /etc/profile.d/conda.sh \
    && find /opt/conda/ -follow -type f -name '*.a' -delete \
    && find /opt/conda/ -follow -type f -name '*.js.map' -delete \
    && [ -z "$CONDA_VERSION" ] || /opt/conda/bin/conda install -n base conda=$CONDA_VERSION \
    && /opt/conda/bin/conda clean -afy \
    && groupadd -r conda --gid ${CONDA_GID} \
    && usermod -aG conda ${USERNAME} \
    && chown -R :conda /opt/conda \
    && chmod -R g+w /opt/conda \
    && find /opt -type d | xargs -n 1 chmod g+s

# Add a section to /etc/bash.bashrc that ensures that a section is present at the end of ~/.bashrc.
# We can't just write to .bashrc from here because it will be overwritten if the vscode user has
# opted to use their own dotfiles repo. The dotfiles repo is cloned after the postCreateCommand
# in the devcontainer.json file is executed.
RUN echo "\n\
if ! grep -q \"^source /opt/conda/etc/profile.d/conda.sh\" ${HOME}/.bashrc; then\n\
	echo \"source /opt/conda/etc/profile.d/conda.sh\" >> ${HOME}/.bashrc\n\
	echo \"conda activate ${CONDA_ENVIRONMENT_NAME}\" >> ${HOME}/.bashrc\n\
fi\n" >> /etc/bash.bashrc

# Create a conda environment from the environment file in the repo root.
COPY --chown=$USER_UID:conda environment.yml /tmp/build/
RUN umask 0002 && /opt/conda/bin/conda env create -f /tmp/build/environment.yml \
    && sudo chown -R :conda /opt/conda/envs \
    && sudo chown -R :conda ${HOME}/.conda

RUN umask 0002 && mkdir -p /go && chown -R :conda /go
ENV PATH=${PATH}:/home/vscode/go/bin/

WORKDIR /opt/temp
COPY .devcontainer/install-tools.sh .
RUN ./install-tools.sh

RUN apt-get update && export DEBIAN_FRONTEND=noninteractive \
    && apt-get -y install --no-install-recommends iputils-ping

USER vscode:conda
SHELL ["/bin/bash", "-e", "-c"]

# .NET Download symbols
ARG DOTNET_RUNTIME_VERSION=6.0.2
ENV PATH=${PATH}:/home/vscode/.dotnet/tools
RUN umask 0002 \
    && . /opt/conda/etc/profile.d/conda.sh \
    && conda activate ${CONDA_ENVIRONMENT_NAME} \
    && dotnet tool install dotnet-symbol --global \
    && dotnet-symbol --symbols /opt/conda/envs/${CONDA_ENVIRONMENT_NAME}/lib/dotnet/shared/Microsoft.NETCore.App/${DOTNET_RUNTIME_VERSION}/* \
    && dotnet-symbol --symbols /opt/conda/envs/${CONDA_ENVIRONMENT_NAME}/lib/dotnet/shared//Microsoft.AspNetCore.App/${DOTNET_RUNTIME_VERSION}/*

# Install go developer tools
RUN . /opt/conda/etc/profile.d/conda.sh \
    && conda activate ${CONDA_ENVIRONMENT_NAME}; \
    GO_TOOLS="\
        golang.org/x/tools/gopls@latest \
        honnef.co/go/tools/cmd/staticcheck@latest \
        golang.org/x/lint/golint@latest \
        github.com/mgechev/revive@latest \
        github.com/uudashr/gopkgs/v2/cmd/gopkgs@latest \
        github.com/ramya-rao-a/go-outline@latest \
        github.com/go-delve/delve/cmd/dlv@latest \
        github.com/golangci/golangci-lint/cmd/golangci-lint@latest"; \
    echo "${GO_TOOLS}" | xargs -n 1 go install
