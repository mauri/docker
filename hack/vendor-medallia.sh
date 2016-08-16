#!/usr/bin/env bash
set -e

cd "$(dirname "$BASH_SOURCE")/.."
source 'hack/.vendor-helpers.sh'

# get libnetwork packages
clone git github.com/docker/libnetwork 9876affaed02879b20a954b9fc9c75b32e513308 https://github.com/medallia/libnetwork.git
# get runc
clone git github.com/opencontainers/runc 908bd425e51c485cd13aa1af4478f6d52c9766f5 https://github.com/medallia/runc.git
