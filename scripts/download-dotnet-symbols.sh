#!/usr/bin/env bash

# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

# Downloads .NET symbols to allow debugging framework code

set -euo pipefail

find "$(dirname "$(which dotnet)")" -name "*.dll" -exec dotnet symbol --symbols {} \;
