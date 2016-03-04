#!/usr/bin/env bash
set -e

cd "$(dirname "$BASH_SOURCE")/.."
source 'hack/.vendor-helpers.sh'

#get libnetwork packages
clone git github.com/docker/libnetwork 5dd6d68508e5c4aad466f52e4725d63a575b2353 https://github.com/medallia/libnetwork.git
