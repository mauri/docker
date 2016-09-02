#!/usr/bin/env bash
set -e

cd "$(dirname "$BASH_SOURCE")/.."
source 'hack/.vendor-helpers.sh'

# get libnetwork packages
clone git github.com/docker/libnetwork e35d732a2045980a30bae8dde1255f57d4b0aabe https://github.com/medallia/libnetwork.git
# get runc
clone git github.com/opencontainers/runc 1d2c6d23c112c427694b180d73114a447daa0175 https://github.com/medallia/runc.git
