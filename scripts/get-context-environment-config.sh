#! /bin/bash

set -euo pipefail

usage() {
  cat <<EOF

Outputs the environment configuration in JSON format. The environment name and configuration directory
can be overridden by setting the TYGER_ENVIRONMENT_NAME and TYGER_ENVIRONMENT_CONFIG_DIR environment variables respectively.
The default environment name is your git alias and the default config dir is <repo_root>/deploy/config/dev.


Usage: $0 [-e|--expression expression]

Options:
  -e | --expression             The expression to evaluate. Defaults to 'config'
  -o | --output                 The output format. Defaults to 'yaml'
  -h, --help                    Brings up this menu
EOF
}

expression="config"
output="yaml"

while [[ $# -gt 0 ]]; do
  key="$1"

  case $key in
  -e | --expression)
    expression="$2"
    shift 2
    ;;
  -o | --output)
    output="$2"
    shift 2
    ;;
  -h | --help)
    usage
    exit
    ;;
  *)
    echo "ERROR: unknown option \"$key\""
    usage
    exit 1
    ;;
  esac
done

this_dir=$(dirname "${0}")

config_dir="${TYGER_ENVIRONMENT_CONFIG_DIR:-${this_dir}/../deploy/config/microsoft}"
helm_chart_dir=$(readlink -f "${this_dir}/../deploy/helm")

if [[ "$expression" == "config" || "$expression" == config.* || "$expression" == "" ]]; then
  environment_name="${TYGER_ENVIRONMENT_NAME:-}"
  if [[ -z "${environment_name:-}" ]]; then
    if [[ ! "$(git config user.email)" =~ [^@]+ ]]; then
      echo >&2 "Set the TYGER_ENVIRONMENT_NAME environment variable or ensure your git email is set"
      exit 1
    fi
    environment_name="${BASH_REMATCH[0]//[.\-_]/}"
  fi
fi

cd "${config_dir}"
cue export . --out "${output}" -t environmentName="${environment_name:-}" -t tygerHelmChartDir="${helm_chart_dir}" -e "${expression}"
