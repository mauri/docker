#!/usr/bin/env bash
set -e

cd "$(dirname "$BASH_SOURCE")/.."
source 'hack/.vendor-helpers.sh'

#get libnetwork packages
clone git github.com/docker/libnetwork a1c9198a83e1e88e9ffc35d60360211e62df404f https://github.com/medallia/libnetwork.git
