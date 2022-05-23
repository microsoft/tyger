#!/bin/bash

set -eu

"$(dirname "$0")/check-login.sh"

usage()
{
  cat << EOF

Checks if any dependencies have been updated and (optionally) opens PR

Usage: $0 [options]

Options:
  --pr                        Open PR if changes have been detected
  -h, --help  Brings up this menu
EOF
}

while [[ $# -gt 0 ]]; do
  key="$1"

  case $key in
    --pr)
      open_pr=1
      shift
      ;;
    -h|--help)
      usage
      exit
      ;;
    *)
      echo "ERROR: unknown option \"$key\""
      usage
      exit 1
      ;;
  esac
done

dependency_manifest="$(dirname "$0")/../dependencies.json"
dependencies="$(jq -r '.dependencies[].name' "$dependency_manifest")"

# Create an empty manifest and add updated dependencies one by one
updated_manifest="{\"dependencies\": []}"
for d in $dependencies; do
  dep="$(jq --arg depname "$d" -r '.dependencies[] | select(.name == $depname )' "$dependency_manifest")"
  type="$(echo "$dep" | jq -r .type)"
  if [[ "$type" == "acrImage" ]]; then
    repository="$(echo "$dep" | jq -r .repository)"
    if [[ "$repository" =~ ([^/]+)/(.*) ]]; then
      acr="${BASH_REMATCH[1]}"
      repo="${BASH_REMATCH[2]}"

      # Of the last 10 manifest (order latest first) select the tags that are 40 characters long (likely to be hashes), grab the top one of those
      tag_candidate="$(az acr repository show-manifests -n "$acr" --repository "$repo" 2>/dev/null --orderby time_desc --query '[:10].tags[]' | jq -r '.[] | select(. | length >= 40)' | head -n 1)"
      if [[ ! "$tag_candidate" =~ ^[a-f0-9]{40} ]] && [[ ! "$tag_candidate" =~ ^[0-9]+.[0-9]+.[0-9]+-[[a-f0-9]{40} ]]; then
        echo "Tag $tag_candidate is not a SHA1 hash or a semantic version with a SHA1 hash"
        exit 1
      fi
      dep="$(echo "$dep" | jq --arg t "$tag_candidate" '.tag = $t')"
    else
      echo "Malformed ACR repository: $repository"
      exit 1
    fi
  else
    echo "Unknown dependency type: $type"
    exit 1
  fi

  # This beauty appends the json object (from the string) to the dependencies
  updated_manifest="$(echo "$updated_manifest" | jq --arg d "$dep" '.dependencies[.dependencies | length] |= ($d | fromjson)')"
done

echo "$updated_manifest" > "$dependency_manifest"

if [[ -n "$(git diff "$dependency_manifest")" && -n "${open_pr:-}" ]]; then
  if [[ -z "$(git config --get user.name)" ]]; then
    git config --local user.name "Michael Hansen"
  fi

  if [[ -z "$(git config --get user.email)" ]]; then
    git config --local user.name "mihansen@microsoft.com"
  fi

  current_branch="$(git branch --show-current)"
  branch_name="dependency-update/$(sha1sum "$dependency_manifest" | awk '{ print $1 }')"

  # Create a local branch if it doesn't exist
  if ! git branch | grep -q "$branch_name"; then
    if [[ -n "$(git diff --staged)" ]]; then
      echo "Error: you have staged changed in your branch"
      exit 1
    fi
    git checkout -b "$branch_name"
    git add "$dependency_manifest"
    git commit -m "Updated dependencies"
  else
    git checkout HEAD -- "$dependency_manifest"
  fi

  # Push it if it is not already remote
  git fetch --all
  if ! git branch -r | grep -q "$branch_name"; then
    git push -u origin "${branch_name}:${branch_name}"
  fi

  # Set up the PR if it is not already there
  if [[ "$(az repos pr list --organization "https://dev.azure.com/msresearch" --project compimag --repository tyger --query "[?contains(@.sourceRefName, '$branch_name')] | length(@)")" == "0" ]]; then
    az repos pr create --organization "https://dev.azure.com/msresearch" --project compimag --repository tyger --source-branch "$branch_name" --reviewers "compimag Team" --squash true --delete-source-branch true --title "$branch_name"
  else
    echo "PR is already active, will not create"
  fi

  # $current_branch could be empty, so we check that before trying to check back out
  if [[ -n "$current_branch" && "$(git branch --show-current)" != "$current_branch" ]]; then
    git checkout "$current_branch"
  fi
fi
