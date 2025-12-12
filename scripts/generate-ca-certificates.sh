#!/usr/bin/env bash

# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

# This script fetches all currently accepted Microsoft root CA certificates
# and outputs them as a single PEM file.

set -euo pipefail

if [ $# -ne 1 ]; then
    echo "Usage: $0 <output-pem-file>" >&2
    exit 1
fi

output_file="$1"

# CSV endpoint containing Microsoft root CA certificate metadata
csv_url="https://ccadb.my.salesforce-sites.com/microsoft/IncludedCACertificateReportForMSFTCSV"

# Create a temporary directory for intermediate files
temp_dir=$(mktemp -d)
trap 'rm -rf "$temp_dir"' EXIT

# Extract existing certificates from output file if it exists and is not empty
declare -A cached_certs
if [ -f "$output_file" ] && [ -s "$output_file" ]; then
    echo "Reading cached certificates from $output_file..." >&2

    # Split the existing PEM file into individual certificates
    csplit -z -f "$temp_dir/existing_" -b "%03d.pem" "$output_file" '/-----BEGIN CERTIFICATE-----/' '{*}' >&/dev/null || true

    for cert_file in "$temp_dir"/existing_*.pem; do
        [ -f "$cert_file" ] || continue
        # Calculate SHA-256 fingerprint of the certificate
        fingerprint=$(openssl x509 -in "$cert_file" -noout -fingerprint -sha256 2>/dev/null | \
            sed 's/.*=//; s/://g' | tr '[:lower:]' '[:upper:]')
        if [ -n "$fingerprint" ]; then
            # Store path to cached cert file, we'll copy it later only if needed
            cached_certs["$fingerprint"]="$cert_file"
        fi
    done

    echo "Found ${#cached_certs[@]} cached certificates" >&2
fi

# Download the CSV metadata
echo "Downloading certificate metadata..." >&2
curl -s "$csv_url" -o "$temp_dir/certs.csv"

# Parse header to find column positions dynamically
header=$(head -1 "$temp_dir/certs.csv")
status_col=$(echo "$header" | awk -F'","' '{for(i=1;i<=NF;i++) if($i ~ /Microsoft Status/) print i}')
fingerprint_col=$(echo "$header" | awk -F'","' '{for(i=1;i<=NF;i++) if($i ~ /SHA-256 Fingerprint/) print i}')
eku_col=$(echo "$header" | awk -F'","' '{for(i=1;i<=NF;i++) if($i ~ /Microsoft EKUs/) print i}')

if [ -z "$status_col" ] || [ -z "$fingerprint_col" ] || [ -z "$eku_col" ]; then
    echo "Error: Could not find required columns in CSV header" >&2
    echo "  Microsoft Status column: ${status_col:-not found}" >&2
    echo "  SHA-256 Fingerprint column: ${fingerprint_col:-not found}" >&2
    echo "  Microsoft EKUs column: ${eku_col:-not found}" >&2
    exit 1
fi

echo "Extracting included certificate fingerprints..." >&2

# Use awk to extract SHA-256 fingerprints for certificates where:
# - Microsoft Status is "Included"
# - Microsoft EKUs contains "Server Authentication"
tail -n +2 "$temp_dir/certs.csv" | \
    awk -F'","' -v status_col="$status_col" -v fp_col="$fingerprint_col" -v eku_col="$eku_col" '
        $status_col ~ /Included/ && $eku_col ~ /Server Authentication/ {
            gsub(/"/, "", $fp_col)
            print $fp_col
        }
    ' | sort > "$temp_dir/fingerprints.txt"

total=$(wc -l < "$temp_dir/fingerprints.txt")
echo "Found $total included certificates" >&2

# Download each certificate and append to output
max_retries=5
retry_delay=2

count=0
downloaded=0
while IFS= read -r sha256; do
    count=$((count + 1))

    # Check if certificate is already cached
    cached_file="${cached_certs[$sha256]:-}"
    if [ -n "$cached_file" ]; then
        echo "Certificate $count/$total: $sha256 (cached)" >&2
        # Copy cached cert to output staging area
        cp "$cached_file" "$temp_dir/pem_$sha256.pem"
        continue
    fi

    echo "Downloading certificate $count/$total: $sha256" >&2

    # Download the PEM from crt.sh with retries
    cert=""
    for attempt in $(seq 1 $max_retries); do
        if cert=$(curl -sf "https://crt.sh/?d=$sha256"); then
            break
        fi

        if [ "$attempt" -lt "$max_retries" ]; then
            echo "  Attempt $attempt failed, retrying in ${retry_delay}s..." >&2
            sleep $retry_delay
            retry_delay=$((retry_delay * 2))  # Exponential backoff
        else
            echo "Error: Failed to download certificate $sha256 after $max_retries attempts" >&2
            exit 1
        fi
    done
    retry_delay=2  # Reset for next certificate

    # Store in temp file with fingerprint as filename for deterministic ordering
    # Use printf to avoid adding extra newlines
    printf '%s\n' "$cert" > "$temp_dir/pem_$sha256.pem"

    # Validate that the downloaded certificate has the expected fingerprint
    actual_fingerprint=$(openssl x509 -in "$temp_dir/pem_$sha256.pem" -noout -fingerprint -sha256 2>/dev/null | \
        sed 's/.*=//; s/://g' | tr '[:lower:]' '[:upper:]')
    if [ "$actual_fingerprint" != "$sha256" ]; then
        echo "Error: Certificate fingerprint mismatch for $sha256" >&2
        echo "  Expected: $sha256" >&2
        echo "  Actual:   $actual_fingerprint" >&2
        exit 1
    fi

    downloaded=$((downloaded + 1))
done < "$temp_dir/fingerprints.txt"

# Concatenate all PEM files in sorted order (by fingerprint)
echo "Combining certificates..." >&2
find "$temp_dir" -maxdepth 1 -name 'pem_*.pem' -print0 | sort -z | while IFS= read -r -d '' pem_file; do
    cat "$pem_file"
done > "$output_file"

echo "Output contains $total certificates ($downloaded downloaded, $((total - downloaded)) cached)" >&2
