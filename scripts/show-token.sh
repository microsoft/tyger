#!/bin/bash

# Your JWT token
jwt=$(az account get-access-token | jq -r '.accessToken')

# Extract the payload
payload=$(echo $jwt | cut -d "." -f 2)

# Fix Base64 padding issues by appending '=' characters
remainder=$((${#payload} % 4))
if [ $remainder -eq 2 ]; then payload="${payload}=="
elif [ $remainder -eq 3 ]; then payload="${payload}="
fi

# Decode from Base64 and parse with jq
echo $payload | base64 -d | jq
