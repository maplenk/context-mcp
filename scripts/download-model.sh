#!/bin/bash
set -euo pipefail

MODEL_DIR="${1:-models/CodeRankEmbed-onnx-int8}"
HF_BASE="https://huggingface.co/mrsladoje/CodeRankEmbed-onnx-int8/resolve/main"

FILES=(
    "onnx/model.onnx"
    "tokenizer.json"
    "config.json"
    "vocab.txt"
    "special_tokens_map.json"
    "tokenizer_config.json"
)

mkdir -p "$MODEL_DIR/onnx"

echo "Downloading CodeRankEmbed ONNX INT8 model to $MODEL_DIR ..."
for f in "${FILES[@]}"; do
    dest="$MODEL_DIR/$f"
    if [ -f "$dest" ]; then
        echo "  [skip] $f (already exists)"
        continue
    fi
    echo "  [download] $f"
    curl -fSL -o "$dest" "$HF_BASE/$f"
done

echo ""
echo "Model downloaded to $MODEL_DIR"
echo ""
echo "Usage:"
echo "  qb-context -onnx-model $MODEL_DIR -onnx-lib /path/to/libonnxruntime.dylib"
echo ""
echo "If the model is at the default path (models/CodeRankEmbed-onnx-int8),"
echo "it will be auto-detected and you only need to specify the ONNX Runtime library:"
echo "  qb-context -onnx-lib /path/to/libonnxruntime.dylib"
