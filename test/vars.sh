#!/bin/bash

## ===== Environment variables for the provider-valkey integration tests =====
export PROVIDER_ROOT_PATH=${PROVIDER_ROOT_PATH:-${PWD}}
echo "PROVIDER_ROOT_PATH=${PROVIDER_ROOT_PATH}"

## Default Valkey engine version exercised by the tests. Keep in sync with the
## provider's default version bundle (definition/versions.yaml).
export VALKEY_ENGINE_VERSION=${VALKEY_ENGINE_VERSION:-"9.0.0"}
echo "VALKEY_ENGINE_VERSION=${VALKEY_ENGINE_VERSION}"

## Default Valkey engine image derived from the version above.
export VALKEY_ENGINE_IMAGE=${VALKEY_ENGINE_IMAGE:-"valkey/valkey:${VALKEY_ENGINE_VERSION}"}
echo "VALKEY_ENGINE_IMAGE=${VALKEY_ENGINE_IMAGE}"
