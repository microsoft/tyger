#!/bin/bash
#
# Compares two container images, ignoring differences in Id, Created, Metadata.LastTagTime, RepoTags, and RepoDigests fields.
# Returns 0 if the two images have been pulled and are equivalent.

set -euo pipefail

if ! image1_metadata=$(docker inspect "${1}") || ! image2_metadata=$(docker inspect "${2}") ; then
  exit 1
fi

jq_query='.[0] | del(.Metadata.LastTagTime, .Id, .Created, .RepoTags, .RepoDigests)'

diff <(echo "${image1_metadata}" | jq "${jq_query}") <(echo "${image2_metadata}" | jq "${jq_query}")
