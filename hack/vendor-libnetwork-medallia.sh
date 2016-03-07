#!/usr/bin/env bash
set -e

cd "$(dirname "$BASH_SOURCE")/.."
source 'hack/.vendor-helpers.sh'

#get libnetwork packages
clone git github.com/docker/libnetwork 2c871751462e8b01f784f203b876ea7b063e87e7 https://github.com/medallia/libnetwork.git
