#! /bin/bash
#
# Outputs the environment configuration in JSON format. The environment name and configuration directory
# can be overridden by setting the TYGER_ENVIRONMENT_NAME and TYGER_ENVIRONMENT_CONFIG_DIR environment variables respectively.
# The default environment name is your git alias and the default config dir is <repo_root>/deploy/config/dev.

set -euo pipefail

this_dir=$(dirname "${0}")

config_dir="${TYGER_ENVIRONMENT_CONFIG_DIR:-${this_dir}/../deploy/config/dev}"

environment_name="${TYGER_ENVIRONMENT_NAME:-}"
if [[ -z "${environment_name:-}" ]]; then
    if [[ ! "$(git config user.email)" =~ [^@]+ ]]; then
        >&2 echo "git email is not set"
        exit 1
    fi
    environment_name="${BASH_REMATCH[0]//[.\-_]/}"
fi

cd "${config_dir}"
cue export . -t environment="${environment_name}"
