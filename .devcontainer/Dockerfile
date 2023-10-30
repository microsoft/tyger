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

# Set devcontainer features environment variables https://containers.dev/implementors/features/#user-env-var
ARG _REMOTE_USER=${USERNAME}
ARG _CONTAINER_USER=${USERNAME}
ARG _REMOTE_USER_HOME=/home/${USERNAME}
ARG _CONTAINER_USER_HOME=/home/${USERNAME}

ARG USER_UID=1000
ARG USER_GID=$USER_UID
ARG CONDA_GID=900
ARG CONDA_ENVIRONMENT_NAME=tyger

RUN groupadd -r conda --gid ${CONDA_GID} \
    && usermod -aG conda ${USERNAME}

ENV GOROOT="/usr/local/go"
ENV GOPATH="/go"
ENV "PATH"="/usr/local/go/bin:/go/bin:/usr/share/dotnet:${PATH}"

RUN umask 0002 \
    &&  git clone https://github.com/devcontainers/features.git /opt/features \
    && cd /opt/features \
    && git checkout 4fca96b5e8a4bfc93679098cb19d73c65ce571eb \
    # DOCKER
    && cd /opt/features/src/docker-outside-of-docker \
    && MOBY=false VERSION=24.0.6 DOCKERDASHCOMPOSEVERSION=none bash install.sh \
    # AZURE CLI
    && cd /opt/features/src/azure-cli \
    && VERSION=2.53.1 bash install.sh \
    # POWERSHELL
    && cd /opt/features/src/powershell \
    && VERSION=7.3.9 bash install.sh \
    # KUBECTL
    && cd /opt/features/src/kubectl-helm-minikube \
    && VERSION=1.28.3 HELM=3.13.1 MINIKUBE=none bash install.sh \
    # DOTNET
    && cd /opt/features/src/dotnet \
    && VERSION=7.0.403 bash install.sh \
    # GO
    && cd /opt/features/src/go \
    && VERSION=1.21.3 bash install.sh \
    && chown -R "${USERNAME}:conda" "${GOROOT}" "${GOPATH}" \
    # cleanup
    && rm -rf /opt/features

# Setting the ENTRYPOINT to docker-init.sh will configure non-root access to
# the Docker socket if "overrideCommand": false is set in devcontainer.json.
# The script will also execute CMD if you need to alter startup behaviors.
ENTRYPOINT [ "/usr/local/share/docker-init.sh" ]
CMD [ "sleep", "infinity" ]

ARG MAMBAFORGE_VERSION=23.3.1-1

# Based on https://github.com/conda-forge/miniforge-images/blob/master/ubuntu/Dockerfile
RUN wget --no-hsts --quiet https://github.com/conda-forge/miniforge/releases/download/${MAMBAFORGE_VERSION}/Mambaforge-${MAMBAFORGE_VERSION}-Linux-$(uname -m).sh -O /tmp/miniforge.sh \
    && /bin/bash /tmp/miniforge.sh -b -p /opt/conda \
    && rm /tmp/miniforge.sh \
    && /opt/conda/bin/conda clean --tarballs --index-cache --packages --yes \
    && find /opt/conda -follow -type f -name '*.a' -delete \
    && find /opt/conda -follow -type f -name '*.pyc' -delete \
    && /opt/conda/bin/conda clean --force-pkgs-dirs --all --yes  \
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
RUN umask 0002 && ./install-tools.sh

USER vscode:conda

USER root

# Add a file that is to be sourced from .bashrc and from the devops pipeline stages
COPY --from=file-normalizer /data/devcontainer.bashrc /opt/devcontainer/

# Add a section to /etc/bash.bashrc that ensures that a section is present at the end of ~/.bashrc.
# We can't just write to .bashrc from here because it will be overwritten if the devcontainer user has
# opted to use their own dotfiles repo. The dotfiles repo is cloned after the postCreateCommand
# in the devcontainer.json file is executed.
SHELL ["/bin/bash", "-e", "-c"]
RUN echo -e "\n\
if ! grep -q \"^source /opt/devcontainer/devcontainer.bashrc\" \${HOME}/.bashrc; then\n\
	echo \"source /opt/devcontainer/devcontainer.bashrc\" >> \${HOME}/.bashrc\n\
fi\n" >> /etc/bash.bashrc
