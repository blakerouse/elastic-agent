#!/usr/bin/env bash
set -euo pipefail

source .buildkite/scripts/common.sh

STACK_PROVISIONER="${1:-"stateful"}"

# Override the stack version from `.package-version` contents
# There is a time when the current snapshot is not available on cloud yet, so we cannot use the latest version automatically
# This file is managed by an automation (mage integration:UpdateAgentPackageVersion) that check if the snapshot is ready.

STACK_VERSION="$(cat .package-version)"
if [[ -n "$STACK_VERSION" ]]; then
    STACK_VERSION=${STACK_VERSION}"-SNAPSHOT"
fi

# Generate the integration test pipeline
AGENT_STACK_VERSION="${STACK_VERSION}" STACK_PROVISIONER="$STACK_PROVISIONER" SNAPSHOT=true mage integration:buildkite > buildkite.yml

# Debug output the pipeline
echo "--- START BUILDKITE GENERATED PIPELINE ---"
cat buildkite.yml
echo "--- END BUILDKITE GENERATED PIPELINE ---"

# Upload the pipeline
buildkite-agent pipeline upload buildkite.yml