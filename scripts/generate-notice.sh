#!/usr/bin/env bash

# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

# This script generates the NOTICE.txt file for the repository using the the go-licenses tool
# for Go dependencies and the ClearlyDefined API for C# dependencies.

set -euo pipefail

export LC_ALL=C

repo_root=$(readlink -f "$(dirname "$0")/..")

cd "$repo_root"

go_sum_relative_path="cli/go.sum"
csharp_package_lock_relative_path="server/Tyger.Server/packages.lock.json"
notice_relative_path="NOTICE.txt"

notice_metadata_path="$repo_root/.notice-metadata.txt"
expected_notice_metadata_path="/tmp/expected-notice-metadata.txt"

this_script_relative_path=$(realpath --relative-to="$repo_root" "$0")

generate_expected_notice_metadata() {
    sha256sum "$go_sum_relative_path" "$csharp_package_lock_relative_path" "$this_script_relative_path" "$notice_relative_path" > "$expected_notice_metadata_path"
}

generate_expected_notice_metadata

if cmp -s "$notice_metadata_path" "$expected_notice_metadata_path"; then
    echo "NOTICE.txt is up to date"
    exit
fi

make -s build

# the file we will be writing to
notice_path="$repo_root/$notice_relative_path"

cd "$repo_root/cli"

# the header of the notice file
cat >"$notice_path" <<-EOM
NOTICES

This repository incorporates material as listed below or described in the code.

EOM

# Go dependencies

go install github.com/google/go-licenses@v1.6.0

save_dir="/tmp/licenses"

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

for lib in $(jq -r '.libraries | to_entries | map(select(.value.type == "package") | .key) | sort[]' server/Tyger.Server/obj/project.assets.json); do
    {
        echo -e "================================================================================\n"

        curl -sS --fail -X POST "https://api.clearlydefined.io/notices" -H "accept: application/json" -H "Content-Type: application/json" -d "{\"coordinates\":[\"nuget/nuget/-/$lib\"],\"options\":{}}" \
            | jq -r '.content'

        echo ""

    } >>"$notice_path"
done

generate_expected_notice_metadata

mv "$expected_notice_metadata_path" "$notice_metadata_path"
