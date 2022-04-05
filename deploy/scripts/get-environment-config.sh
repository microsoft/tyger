#! /bin/bash

set -eu

usage()
{
  cat << EOF

Gets the JSON configuration of an environment.

Usage: $0 [options]

Options:
  --dir,-d <config dir>         The cue configuration directory
  --environment,e <environment> The environment name
  -h, --help                    Brings up this menu
EOF
}


while [[ $# -gt 0 ]]; do
  key="$1"

  case $key in
    --dir|-d)
      config_dir="${2}"
      shift
      shift
      ;;
    --environment|-e)
      environment_name="${2}"
      shift
      shift
      ;;
    -h|--help)
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

if [[ -z "${config_dir:-}" ]]; then
  echo "Error: --dir argument missing"
  exit 1
fi

if [[ -z "${environment_name:-}" ]]; then
  echo "Error: --environment argument missing"
  exit 1
fi

cd "${config_dir}"
cue export . -t environment="${environment_name}"
