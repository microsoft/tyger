#!/usr/bin/env bash

# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

# Downloads .NET symbols to allow debugging framework code

set -euo pipefail

if ! command -v dotnet-symbol &> /dev/null; then
    dotnet tool install dotnet-symbol --global --configfile "$(dirname "$0")/../NuGet.Config"
fi

dotnet_symbol_path=$(which dotnet-symbol)
sudo "$dotnet_symbol_path" --symbols /usr/share/dotnet/ --recurse-subdirectories
