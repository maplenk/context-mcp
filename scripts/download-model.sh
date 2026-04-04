#!/bin/bash
set -euo pipefail

MODEL_DIR="${1:-models/jina-code-int8}"
RELEASE_URL="https://github.com/naman/qb-context/releases/download/v0.2.0/context-mcp-model-jina-code-int8.tar.gz"

mkdir -p "$MODEL_DIR"
echo "Downloading pre-quantized Jina Code model (~488MB)..."
curl -fSL "$RELEASE_URL" | tar -xzf - -C "$MODEL_DIR"
echo "Model downloaded to $MODEL_DIR"
echo "Usage: qb-context -onnx-model $MODEL_DIR -onnx-lib /path/to/libonnxruntime.dylib"
