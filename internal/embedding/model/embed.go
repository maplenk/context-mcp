package model

// Model metadata for the Qwen2-based code embedding model.
// The ONNX model and tokenizer are loaded from disk at runtime via
// --onnx-model flag; they are NOT embedded in the binary.

const ModelName = "qwen2-code-embedding"
const ModelFullDim = 896        // full hidden_size of the Qwen2 model
const ModelQuantization = "INT8" // quantization format (QOperator)

// MatryoshkaDims lists the supported Matryoshka embedding dimensions.
var MatryoshkaDims = []int{64, 128, 256, 512, 896}

// DefaultMatryoshkaDim is the recommended dimension balancing quality vs storage.
const DefaultMatryoshkaDim = 256
