#! /bin/bash
#
# Downloads .NET symbols to allow debugging framework code

set -euo pipefail

find "$(dirname "$(which dotnet)")" -name "*.dll" -exec dotnet symbol --symbols {} \;
