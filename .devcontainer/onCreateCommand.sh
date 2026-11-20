#!/bin/bash
set -o errexit
set -o nounset
set -o xtrace

# Install Node (matching the root .node-version) and the Go toolchain
# (matching server-go/go.mod) via mise.
mise use --global node@26
mise use --global go@1.27.4

# Install pnpm globally through npm. Node 26 no longer bundles corepack, so
# this is the supported install path; the version must match the
# packageManager field in package.json.
npm install -g pnpm@12.21.0

# Install Claude Code
#curl -fsSL https://claude.ai/install.sh | bash
