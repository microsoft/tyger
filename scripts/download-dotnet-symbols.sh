#! /bin/bash
#
# Downloads .NET symbols to allow debugging framework code

set -euo pipefail

find "${CONDA_PREFIX}/lib/dotnet/shared/" -name "*.dll" -exec dotnet symbol --symbols {} \;
