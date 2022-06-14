#!/bin/bash
set -euo pipefail

this_dir=$(dirname "${0}")
cd "${this_dir}/../eminence/gadgetron/" && python3 get_cases.py -v
