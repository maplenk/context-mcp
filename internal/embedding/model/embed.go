package model

// Default embedding model: CodeRankEmbed (NomicBERT architecture, INT8 quantized).
// The ONNX model and tokenizer are loaded from disk at runtime via
// --onnx-model flag; they are NOT embedded in the binary.

// DefaultEmbeddingDim is the recommended ONNX embedding dimension (CodeRankEmbed full).
const DefaultEmbeddingDim = CodeRankEmbedFullDim

// CodeRankEmbed model metadata (primary/default model)
const CodeRankEmbedModelName = "CodeRankEmbed"
const CodeRankEmbedFullDim = 768
const CodeRankEmbedQuantization = "INT8-dynamic"

// Qwen2/Jina model metadata (legacy/alternative)
const ModelName = "qwen2-code-embedding"
const ModelFullDim = 896        // full hidden_size of the Qwen2 model
const ModelQuantization = "INT8" // quantization format (QOperator)

// MatryoshkaDims lists the supported Matryoshka embedding dimensions for Qwen2.
var MatryoshkaDims = []int{64, 128, 256, 512, 896}

// DefaultMatryoshkaDim is the recommended Qwen2 Matryoshka dimension.
const DefaultMatryoshkaDim = 256
