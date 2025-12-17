ARG DEVCONTAINER_BASE=mcr.microsoft.com/devcontainers/base:1.0.9-ubuntu-22.04

FROM ${DEVCONTAINER_BASE} AS devcontainer

# Install needed packages and setup non-root user.
ARG USERNAME="vscode"

# Set devcontainer features environment variables https://containers.dev/implementors/features/#user-env-var
ARG _REMOTE_USER=${USERNAME}
ARG _CONTAINER_USER=${USERNAME}
ARG _REMOTE_USER_HOME=/home/${USERNAME}
ARG _CONTAINER_USER_HOME=/home/${USERNAME}

ARG USER_UID=1000
ARG USER_GID=$USER_UID

ENV DOTNET_ROOT="/usr/share/dotnet"
ENV DOTNET_RUNNING_IN_CONTAINER="true"
ENV GOROOT="/usr/local/go"
ENV GOPATH="/go"
ENV "PATH"="/usr/local/go/bin:/go/bin:${DOTNET_ROOT}:/home/${USERNAME}/.dotnet/tools:/usr/local/share/nvm/current/bin:${PATH}"
ENV NVM_DIR="/usr/local/share/nvm"
ENV NVM_SYMLINK_CURRENT="true"

RUN umask 0002 \
    &&  git clone https://github.com/devcontainers/features.git /opt/features\
    && cd /opt/features \
    && git checkout eea561a9ad45c383c2d9832d2da031410be8b5a0 \
    ## NODE
    && cd /opt/features/src/node \
    && VERSION=24.8.0 bash install.sh \
    # DOCKER
    && cd /opt/features/src/docker-outside-of-docker \
    && MOBY=false VERSION=28.4.0 DOCKERDASHCOMPOSEVERSION=none bash install.sh \
    # AZURE CLI
    && cd /opt/features/src/azure-cli \
    && VERSION=2.77.0 bash install.sh \
    # POWERSHELL
    && cd /opt/features/src/powershell \
    && VERSION=7.5.3 bash install.sh \
    # KUBECTL
    && cd /opt/features/src/kubectl-helm-minikube \
    && VERSION=1.34.1 HELM=3.16.4 MINIKUBE=none bash install.sh \
    # DOTNET
    && cd /opt/features/src/dotnet \
    && VERSION=9.0.308 bash install.sh \
    # GO
    && cd /opt/features/src/go \
    && VERSION=1.25.5 bash install.sh \
    && chown -R "${USERNAME}" "${GOROOT}" "${GOPATH}" \
    # GitHub CLI
    && cd /opt/features/src/github-cli/ \
    && VERSION=2.79.0 bash install.sh \
    # cleanup
    && rm -rf /opt/features

# Setting the ENTRYPOINT to docker-init.sh will configure non-root access to
# the Docker socket if "overrideCommand": false is set in devcontainer.json.
# The script will also execute CMD if you need to alter startup behaviors.
ENTRYPOINT [ "/usr/local/share/docker-init.sh" ]
CMD [ "sleep", "infinity" ]

WORKDIR /tmp
COPY install-tools.sh .
RUN ./install-tools.sh

# Add a file that is to be sourced from .bashrc and from the devops pipeline stages
COPY devcontainer.bashrc /opt/devcontainer/

# Add a section to /etc/bash.bashrc that ensures that a section is present at the end of ~/.bashrc.
# We can't just write to .bashrc from here because it will be overwritten if the devcontainer user has
# opted to use their own dotfiles repo. The dotfiles repo is cloned after the postCreateCommand
# in the devcontainer.json file is executed.
SHELL ["/bin/bash", "-e", "-c"]
RUN echo -e "\n\
if ! grep -q \"^source /opt/devcontainer/devcontainer.bashrc\" \${HOME}/.bashrc; then\n\
	echo \"source /opt/devcontainer/devcontainer.bashrc\" >> \${HOME}/.bashrc\n\
fi\n" >> /etc/bash.bashrc

RUN mkdir -p /workspaces && ln -s /workspaces/tyger /workspaces/tyger-docker
