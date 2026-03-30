package model

// This package will contain the embedded ONNX model and tokenizer vocabulary
// when the ONNX-based embedder is implemented.
//
// Usage (future):
//
//	//go:embed all-MiniLM-L6-v2-int8.onnx
//	var ModelData []byte
//
//	//go:embed vocab.txt
//	var VocabData []byte
//
// For now, the HashEmbedder in the parent package provides a functional
// fallback that generates deterministic pseudo-embeddings.

// Placeholder to ensure the package compiles
const ModelName = "all-MiniLM-L6-v2"
const ModelDimension = 384
const ModelQuantization = "INT8"
