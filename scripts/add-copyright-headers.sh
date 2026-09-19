#!/usr/bin/env bash

# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

# Ensures that all source files have the right copyright header

set -euo pipefail

shopt -s globstar nullglob dotglob

process_files() {
    header_line_length=$(echo "$header" | wc -l)

    for file in "${files[@]}"; do
        if [[ ! -f "$file" || "$(head -n "$header_line_length" "$file")" == "$header" ]]; then
            continue
        fi

        if [[ "$(head -n 1 "$file")" == '#!'* ]]; then
            if [[ "$(tail -n +2 "$file" | head -n "$header_line_length")" == "$header" ||
                "$(tail -n +3 "$file" | head -n "$header_line_length")" == "$header" ]]; then
                continue
            fi
        fi

        # Snapshot permissions
        perms=$(stat -c %a "$file")

        echo -e "$header\n" | cat - "$file" >temp && mv temp "$file"

        # Restore permissions
        chmod "$perms" "$file"
    done
}

# C# and Go
header=$'// Copyright (c) Microsoft Corporation.\n// Licensed under the MIT License.'
files=(**/*.{cs,go})
process_files

# Bash
header=$'#!/usr/bin/env bash\n\n# Copyright (c) Microsoft Corporation.\n# Licensed under the MIT License.'
files=(**/*.sh)
process_files

# PowerShell
header=$'# Copyright (c) Microsoft Corporation.\n# Licensed under the MIT License.'
files=(**/*.{ps1,psm1})
process_files

# Makefile
header=$'# Copyright (c) Microsoft Corporation.\n# Licensed under the MIT License.'
files=(**/Makefile)
process_files
