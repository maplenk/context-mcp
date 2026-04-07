#!/usr/bin/env bash
# Install git hooks for context-mcp
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"

echo "Installing git hooks..."
git -C "$REPO_ROOT" config core.hooksPath "$REPO_ROOT/.githooks"

echo "✓ Git hooks installed from .githooks/"
echo "  Pre-push: Version change → benchmark prompt"
echo ""
echo "To skip benchmarks on a single push: CONTEXT_MCP_SKIP_BENCH=1 git push"
echo "To disable hooks entirely: git push --no-verify"
