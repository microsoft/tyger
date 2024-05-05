#!/usr/bin/env bash

# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

# Ensures that all source files have the right copyright header

set -euo pipefail

shopt -s globstar nullglob dotglob

process_files() {
    header_line_length=$(echo "$header" | wc -l)

    for file in "${files[@]}"; do
        if [[ "$(head -n "$header_line_length" "$file")" != "$header" ]]; then
            # Snapshot permissions
            perms=$(stat -c %a "$file")

            echo -e "$header\n" | cat - "$file" >temp && mv temp "$file"

            # Restore permissions
            chmod "$perms" "$file"
        fi
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
