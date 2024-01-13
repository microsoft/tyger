#! /bin/bash

# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

# This script generates the NOTICE file for the repository based on the GO dependencies
# using the go-licenses tool.

set -euo pipefail

export LC_ALL=C

repo_root=$(readlink -f "$(dirname "$0")/..")

cd "$repo_root/cli"

save_dir="/tmp/licenses"

# the file we will be writing to
notice_path="$repo_root/NOTICE.txt"

# the header of the notice file
cat >"$notice_path" <<-EOM
NOTICES

This repository incorporates material as listed below or described in the code.

EOM

# Go dependencies

go install github.com/google/go-licenses@v1.6.0

# The tool will write out warnings to stderr for non-go binary dependencies that it cannot follow.
# This pattern will filter the known warnings out of the output.
known_non_go_dependency_patterns="(golang.org/x/sys.*/unix)|(github.com/modern-go/reflect2)|(go.starlark.net)|(github.com/klauspost/compress)|(github.com/cespare/xxhash/v2)"

# github.com/mattn/go-localereader v0.0.1 has no license file

go-licenses save ./... \
    --ignore "github.com/microsoft/tyger/cli" \
    --ignore "github.com/mattn/go-localereader" \
    --save_path=$save_dir --force \
     2> >(grep -Pv "$known_non_go_dependency_patterns")

# license and notice files will be in directories named after the import path of each library

# get the library names from the directory names
lib_names=$(find $save_dir -type f -print0 | xargs -0 realpath --relative-to $save_dir | xargs dirname | sort | uniq)

for lib_name in $lib_names; do
    {
        echo "================================================================================"
        echo -e "\n$lib_name\n"

        notice_pattern="NOTICE*"

        license=$(find "$save_dir/$lib_name" -type f ! -iname "$notice_pattern" -print0 | sort -z | xargs -0 cat)

        if [ -n "$license" ]; then
            echo "$license"
            echo ""
        fi

        notice=$(find "$save_dir/$lib_name" -type f -iname "$notice_pattern" -print0 | sort -z | xargs -0 cat)
        if [ -n "$notice" ]; then
            echo "$notice"
            echo ""
        fi
    } >>"$notice_path"

done


# C# dependencies
cd "$repo_root"
make -s build

for lib in $(jq -r '.libraries | to_entries | map(select(.value.type == "package") | .key) | sort[]' server/Tyger.Server/obj/project.assets.json); do
    {
        echo -e "================================================================================\n"

        curl -sS -X POST "https://api.clearlydefined.io/notices" -H "accept: application/json" -H "Content-Type: application/json" -d "{\"coordinates\":[\"nuget/nuget/-/$lib\"],\"options\":{}}" \
            | jq -r '.content'

        echo ""

    } >>"$notice_path"
done
