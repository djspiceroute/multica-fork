#!/bin/bash
set -e

export HOME=/Users/deepankarjoshi
export MULTICA_CODEX_PATH=/Users/deepankarjoshi/.npm-global/bin/codex
export MULTICA_GEMINI_PATH=/Users/deepankarjoshi/.npm-global/bin/gemini
export MULTICA_CLAUDE_PATH=/Users/deepankarjoshi/.local/bin/claude
export MULTICA_NODE_PATH=/opt/homebrew/bin/node
export PATH="/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:$PATH"

cd /Users/deepankarjoshi/multica/server
exec go run ./cmd/multica daemon start
