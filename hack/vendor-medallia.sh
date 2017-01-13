#!/usr/bin/env bash
set -e

cd "$(dirname "$BASH_SOURCE")/.."
source 'hack/.vendor-helpers.sh'

# get libnetwork packages
clone git github.com/docker/libnetwork "${MEDALLIA_LIBNETWORK_VERSION}" https://github.com/medallia/libnetwork.git
# get runc
clone git github.com/opencontainers/runc "${MEDALLIA_RUNC_VERSION}" https://github.com/medallia/runc.git
