#########################################################
# file-normalizer stage
# In order to use BuildKit remote caching, input files must have
# not only the right content hash, but also the right permissions.
# Git only tracks whether the owner can execute a file.
# Here we bring in all files that are going to be used in the
# subsequent stage and normalize the permissions.
#########################################################

FROM mcr.microsoft.com/oss/busybox/busybox:1.33.1 as file-normalizer

COPY environment.yml \
     .devcontainer/devcontainer.bashrc  \
     .devcontainer/install-tools.sh \
     /data/

RUN chmod -R 555 /data/

#########################################################
# devcontainer stage
# Installs all dependencies and tooling for development.
#########################################################

FROM mcr.microsoft.com/vscode/devcontainers/base:0.201.8-focal AS devcontainer

# Install needed packages and setup non-root user.
ARG USERNAME="vscode"
ARG USER_UID=1000
ARG USER_GID=$USER_UID
ARG CONDA_GID=900
ARG CONDA_ENVIRONMENT_NAME=tyger
ARG VSCODE_DEV_CONTAINERS_SCRIPT_LIBRARY_VERSION=v0.229.0

# Enable non-root Docker access in container
ARG ENABLE_NONROOT_DOCKER="true"
# Use the OSS Moby CLI instead of the licensed Docker CLI
ARG USE_MOBY="false"
RUN script=$(curl -fsSL "https://raw.githubusercontent.com/microsoft/vscode-dev-containers/${VSCODE_DEV_CONTAINERS_SCRIPT_LIBRARY_VERSION}/script-library/docker-debian.sh") && bash -c "$script" -- "${ENABLE_NONROOT_DOCKER}" "/var/run/docker-host.sock" "/var/run/docker.sock" "${USERNAME}" "${USE_MOBY}"

# Setting the ENTRYPOINT to docker-init.sh will configure non-root access to
# the Docker socket if "overrideCommand": false is set in devcontainer.json.
# The script will also execute CMD if you need to alter startup behaviors.
ENTRYPOINT [ "/usr/local/share/docker-init.sh" ]
CMD [ "sleep", "infinity" ]

ARG MAMBA_VERSION=0.22.1

# Based on https://github.com/ContinuumIO/docker-images/blob/master/miniconda3/debian/Dockerfile.
RUN wget --quiet https://repo.anaconda.com/miniconda/Miniconda3-latest-Linux-x86_64.sh -O miniconda.sh \
    && mkdir -p /opt \
    && sh miniconda.sh -b -p /opt/conda \
    && ln -s /opt/conda/etc/profile.d/conda.sh /etc/profile.d/conda.sh \
    && find /opt/conda/ -follow -type f -name '*.a' -delete \
    && find /opt/conda/ -follow -type f -name '*.js.map' -delete \
    && /opt/conda/bin/conda install -n base -c conda-forge mamba=${MAMBA_VERSION} \
    && /opt/conda/bin/conda clean -afy \
    && groupadd -r conda --gid ${CONDA_GID} \
    && usermod -aG conda ${USERNAME} \
    && chown -R :conda /opt/conda \
    && chmod -R g+w /opt/conda \
    && find /opt -type d | xargs -n 1 chmod g+s

# Create a conda environment from the environment file in the repo root.
COPY --from=file-normalizer --chown=$USER_UID:conda /data/environment.yml /tmp/build/
RUN umask 0002 \
    && /opt/conda/bin/mamba env create -f /tmp/build/environment.yml \
    && /opt/conda/bin/mamba clean -fy \
    && sudo chown -R :conda /opt/conda/envs

WORKDIR /opt/temp
COPY --from=file-normalizer /data/install-tools.sh .
RUN ./install-tools.sh

# Install go tools
ARG CUE_VERSION=v0.4.2
SHELL ["/bin/bash", "-e", "-c"]
RUN . /opt/conda/etc/profile.d/conda.sh \
    && conda activate ${CONDA_ENVIRONMENT_NAME}; \
    export GOBIN=/opt/conda/envs/${CONDA_ENVIRONMENT_NAME}/bin; \
    GO_TOOLS="\
        cuelang.org/go/cmd/cue@${CUE_VERSION} \
        golang.org/x/tools/gopls@latest \
        honnef.co/go/tools/cmd/staticcheck@latest \
        golang.org/x/lint/golint@latest \
        github.com/mgechev/revive@latest \
        github.com/uudashr/gopkgs/v2/cmd/gopkgs@latest \
        github.com/ramya-rao-a/go-outline@latest \
        github.com/go-delve/delve/cmd/dlv@latest \
        github.com/golangci/golangci-lint/cmd/golangci-lint@latest"; \
    echo "${GO_TOOLS}" | xargs -n 1 go install

USER vscode:conda

# .NET Download symbols
ARG DOTNET_RUNTIME_VERSION=6.0.2
ENV PATH=${PATH}:/home/vscode/.dotnet/tools
RUN umask 0002 \
    && . /opt/conda/etc/profile.d/conda.sh \
    && conda activate ${CONDA_ENVIRONMENT_NAME} \
    && dotnet tool install dotnet-symbol --global

USER root

# Add a file that is to be sourced from .bashrc and from the devops pipeline stages
COPY --from=file-normalizer /data/devcontainer.bashrc /opt/devcontainer/

# Add a section to /etc/bash.bashrc that ensures that a section is present at the end of ~/.bashrc.
# We can't just write to .bashrc from here because it will be overwritten if the devcontainer user has
# opted to use their own dotfiles repo. The dotfiles repo is cloned after the postCreateCommand
# in the devcontainer.json file is executed.
RUN echo -e "\n\
if ! grep -q \"^source /opt/devcontainer/devcontainer.bashrc\" \${HOME}/.bashrc; then\n\
	echo \"source /opt/devcontainer/devcontainer.bashrc\" >> \${HOME}/.bashrc\n\
fi\n" >> /etc/bash.bashrc
