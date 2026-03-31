# Architectural Blueprint and Implementation Protocol: Local-First Code Context MCP Daemon

## Executive Architectural Overview

The proliferation of Large Language Model (LLM) coding agents, such as Claude Code, Codex, and Cursor, has fundamentally altered the software development lifecycle. However, these tools are consistently bottlenecked by a phenomenon known as context amnesia and the associated high computational cost of token burn. In traditional workflows, developers rely on standard lexical search utilities, including grep or glob, to locate relevant code segments. These utilities operate entirely without semantic or structural awareness, forcing the developer to retrieve and inject massive, often irrelevant, files into the LLM's context window. This brute-force approach degrades the model's long-term reasoning capabilities, dilutes the signal-to-noise ratio of the prompt, and rapidly escalates API costs or local inference resource utilization.

The definitive solution to this limitation is the development of a strictly local, open-source Go daemon that operates continuously in the background to build and maintain a structural graph and a semantic index of a given codebase. By exposing this deeply analyzed state via the Model Context Protocol (MCP), the daemon allows LLMs to autonomously query exact function signatures, mathematically trace the blast radius of proposed modifications, and retrieve surgical context at the byte level. This highly targeted retrieval mechanism saves tokens, preserves the context window for complex reasoning tasks, and transforms the LLM from a passive text generator into an active, structurally aware participant in the codebase.

This report provides an exhaustive architectural blueprint, schema design, and implementation strategy for constructing this context daemon. The system is engineered under strict constraints: it must operate entirely locally with zero cloud dependencies, maintain a memory footprint strictly below 2 GB during active indexing (targeting an idle state of under 200 MB), and deploy as a frictionless, single statically linked Go binary without reliance on external containerization or runtimes.

## Target Audience, Scale, and Ecosystem Constraints

The primary demographic for this tool comprises solo developers and small engineering teams managing complex, interdependent monorepos and microservices. The architectural scale is defined by the volume of interconnected files and the depth of their dependency trees. The daemon is specifically optimized for three primary repository paradigms.

| **Repository Archetype**      | **Characteristics and Scale**                                                                                                          | **Analytical Focus**                                                                                    |
| ----------------------------- | -------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------- |
| **Laravel/Node.js Monorepos** | Approximately 1000 to 2000 files. High usage of object-oriented hierarchies, traits, and complex routing architectures.                | Extracting namespace resolutions, class inheritance graphs, and dynamic method invocations.             |
| ---                           | ---                                                                                                                                    | ---                                                                                                     |
| **React Monorepos**           | Approximately 1000 files. Heavy reliance on functional components, hook dependencies, and component composition.                       | Resolving prop drilling paths, identifying component import structures, and mapping state dependencies. |
| ---                           | ---                                                                                                                                    | ---                                                                                                     |
| **Go Microservices**          | Approximately 300 files distributed across interconnected repositories. Strong typing, structural interfaces, and concurrent patterns. | Mapping interface implementations, struct embedding, and cross-package method calls.                    |
| ---                           | ---                                                                                                                                    | ---                                                                                                     |

To serve this audience effectively, the tool must eliminate deployment friction. The requirement for a single statically linked Go binary precludes the use of Docker containers, Node.js runtime environments, or complex local database server installations. The entire stack-parsing, semantic embedding, relational storage, and graph traversal-must be internalized within the Go application space.

## Technical Constraints and Stack Justification

The technical foundation of the daemon is dictated by the intersection of performance requirements, memory constraints, and deployment simplicity. The maximum permissible active memory footprint is 2 GB, with a strict requirement to idle below 200 MB. This necessitates a language with deterministic memory management, high-performance concurrency, and robust cross-compilation capabilities. Go fulfills these criteria perfectly.

| **Component Layer**   | **Technology Choice**          | **Architectural Justification**                                                                                                                                                       |
| --------------------- | ------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **Host Language**     | Go                             | Provides memory safety, excellent concurrency primitives (goroutines) for parallel AST parsing, and the ability to compile into a static binary.                                      |
| ---                   | ---                            | ---                                                                                                                                                                                   |
| **Structural Parser** | Tree-sitter (via gotreesitter) | Generates Abstract Syntax Trees (ASTs) with extreme speed and low memory overhead. The pure-Go implementation eliminates CGO requirements.<sup>1</sup>                                |
| ---                   | ---                            | ---                                                                                                                                                                                   |
| **State Storage**     | SQLite                         | An embedded, serverless relational database that requires zero configuration. It acts as the resilient single source of truth for the codebase's metadata.                            |
| ---                   | ---                            | ---                                                                                                                                                                                   |
| **Vector Engine**     | sqlite-vec                     | An extension for SQLite that introduces the vec0 virtual table, enabling high-performance K-Nearest Neighbors (KNN) semantic search directly via SQL.<sup>2</sup>                     |
| ---                   | ---                            | ---                                                                                                                                                                                   |
| **Graph Engine**      | gonum/graph                    | A pure-Go library providing in-memory directed graphs. It allows for instant Breadth-First Search (BFS) and PageRank calculations without the overhead of an external graph database. |
| ---                   | ---                            | ---                                                                                                                                                                                   |
| **Semantic Engine**   | all-MiniLM-L6-v2 via ONNX      | A highly efficient sentence transformer model. When quantized (INT8) and run natively in Go via purego, it shrinks to ~22MB and reduces memory usage significantly.                   |
| ---                   | ---                            | ---                                                                                                                                                                                   |
| **MCP Interface**     | mcp-golang                     | Provides a complete toolkit for spinning up an MCP server via stdio transport.                                                                                                        |
| ---                   | ---                            | ---                                                                                                                                                                                   |

## Data Ingestion and Synchronization Pipeline

The operational lifecycle of the daemon begins with the Watcher subsystem, a background process responsible for maintaining continuous synchronization between the host filesystem and the internal structural state.

### Filesystem Event Monitoring and Deduplication

Operating systems emit filesystem events with notorious inconsistency. A single file modification might register as a solitary WRITE event on Linux via inotify, while macOS utilizing FSEvents or BSD using kqueue might emit a sequence of CREATE, CHMOD, and RENAME events for the exact same user action. Relying on raw event streams directly triggers redundant parsing cycles, violating the CPU and memory efficiency constraints.

To mitigate this, the daemon leverages the fsnotify Go library to abstract the underlying OS-level system calls, wrapping it in an intelligent translation layer like github.com/helshabini/fsbroker.<sup>3</sup> This establishes a temporal sliding window. When a burst of events is detected for a specific file path, the deduplication layer analyzes the sequence, effectively coalescing multiple WRITE events or CREATE+RENAME sequences into a single logical "Update" action.<sup>3</sup> This ensures the parser pipeline is only invoked when the filesystem state has settled.

### Intelligent Path Exclusion and Gitignore Evaluation

Attempting to parse an entire repository unrestrictedly will result in the ingestion of massive dependency directories (e.g., node_modules, vendor) and compiled build artifacts. This immediately exhausts the 2 GB memory limit and pollutes the semantic index. The watcher must strictly respect local exclusion rules.

Standard fsnotify does not possess native recursive watching capabilities, nor does it inherently understand Git exclusion patterns. The daemon resolves this by integrating a dedicated pattern-matching engine such as github.com/crackcomm/go-gitignore.<sup>4</sup> During the initial boot phase, the daemon walks the directory tree and evaluates paths against the parsed rules of the repository's .gitignore file, matching Git's internal fnmatch logic.<sup>4</sup> If a directory evaluates to a positive match against an exclusion rule, the filesystem walk prunes that branch entirely, preventing memory bloat.

## Structural Parsing and AST Extraction

When the debounced Watcher signals a valid file modification, the file's byte array is routed to the Parser Pipeline. This component utilizes Tree-sitter to generate an Abstract Syntax Tree (AST) and execute S-expression queries to extract structural nodes (Functions, Classes, Structs) and relational edges (Calls, Imports, Implements).

### Resolving the Tree-sitter CGO Dependency Constraint

Tree-sitter is a state-of-the-art incremental parsing library written in pure C. Traditionally, integrating Tree-sitter into a Go application requires CGO bindings. The reliance on CGO introduces severe complications for cross-compilation and violates the mandate for a frictionless, statically linked deployment.

To achieve true static linking without external C toolchains, the architecture utilizes github.com/odvcencio/gotreesitter, a pure-Go implementation of the Tree-sitter runtime.<sup>1</sup> This library deserializes the exact same binary parse-tables used by the upstream C runtime but implements the parser, lexer, query engine, and an arena memory allocator natively in Go.<sup>1</sup> By utilizing gotreesitter, the daemon entirely eliminates the CGo boundary, allowing the Go compiler to seamlessly cross-compile the application for Windows, macOS, and Linux, while the arena allocator prevents garbage collection thrashing.<sup>1</sup>

### Language-Specific S-Expression Query Formulation

Tree-sitter extracts targeted data from the AST through a highly optimized, Lisp-like query language composed of S-expressions. These expressions define patterns that match specific syntactic structures, utilizing the @capture syntax to tag the resulting nodes for extraction by the Go application.

#### JavaScript and TypeScript Extraction Architecture

Scheme

; Match standard function declarations and capture the name and body  
(function_declaration  
name: (identifier) @node.name.function  
body: (statement_block) @node.body) @node.definition.function  
<br/>; Match arrow functions bound to lexical declarations (const/let)  
(lexical_declaration  
(variable_declarator  
name: (identifier) @node.name.function  
value: (arrow_function) @node.body)) @node.definition.function  
<br/>; Match function invocations to construct the call graph  
(call_expression  
function: (identifier) @edge.call.target) @edge.call  
<br/>; Match member expression calls (e.g., console.log, object.method)  
(call_expression  
function: (member_expression  
object: (identifier) @edge.call.object  
property: (property_identifier) @edge.call.property)) @edge.call

This nuanced querying approach ensures that the daemon captures both the symbol name for lexical indexing and the precise byte offsets of the body block. The byte offsets are critical for the read_symbol MCP tool, which retrieves exact source code without loading the entire file into memory.

#### PHP Extraction Architecture

Scheme

; Capture class definitions and their internal declaration lists  
(class_declaration  
name: (name) @node.name.class  
body: (declaration_list) @node.body) @node.definition.class  
<br/>; Capture methods defined within the class boundary  
(method_declaration  
name: (name) @node.name.method  
body: (compound_statement) @node.body) @node.definition.method  
<br/>; Capture object instantiation to build dependency edges  
(object_creation_expression  
(name) @edge.instantiates.target) @edge.instantiates

#### Go Extraction Architecture

Scheme

; Capture struct type definitions  
(type_spec  
name: (type_identifier) @node.name.struct  
type: (struct_type) @node.body) @node.definition.struct  
<br/>; Capture standard standalone functions  
(function_declaration  
name: (identifier) @node.name.function) @node.definition.function  
<br/>; Capture receiver methods and associate them with their base type  
(method_declaration  
receiver: (parameter_list) @node.receiver  
name: (field_identifier) @node.name.method) @node.definition.method  
<br/>; Capture cross-package function invocations  
(call_expression  
function: (selector_expression  
operand: (identifier) @edge.call.package  
field: (field_identifier) @edge.call.target)) @edge.call

### Optimized Go Data Structures for AST Mapping

To transition the extracted data from the Tree-sitter AST to the SQLite persistence layer and the Gonum graph engine, the intermediate Go structures must be meticulously optimized for minimal memory footprint.

| **Data Structure** | **Field Specification** | **Type** | **Memory Optimization Rationale**                                                                                                                               |
| ------------------ | ----------------------- | -------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **ASTNode**        | ID                      | string   | A SHA-256 hash combining the file path and symbol name guarantees global uniqueness across the monorepo.                                                        |
| ---                | ---                     | ---      | ---                                                                                                                                                             |
| **ASTNode**        | FilePath                | string   | The relative path to the source file on disk.                                                                                                                   |
| ---                | ---                     | ---      | ---                                                                                                                                                             |
| **ASTNode**        | SymbolName              | string   | The extracted identifier (e.g., calculateTotal).                                                                                                                |
| ---                | ---                     | ---      | ---                                                                                                                                                             |
| **ASTNode**        | NodeType                | uint8    | An enumeration (Function, Class, Struct) represented as a single byte rather than a heavy string.                                                               |
| ---                | ---                     | ---      | ---                                                                                                                                                             |
| **ASTNode**        | StartByte               | uint32   | Represents the starting offset. Using uint32 instead of standard int64 cuts memory usage in half while still safely supporting source files up to 4 GB in size. |
| ---                | ---                     | ---      | ---                                                                                                                                                             |
| **ASTNode**        | EndByte                 | uint32   | Represents the ending offset of the node body.                                                                                                                  |
| ---                | ---                     | ---      | ---                                                                                                                                                             |
| **ASTNode**        | ContentSum              | string   | A truncated, concatenated string of the symbol signature and its docstring, explicitly prepared for the embedding engine.                                       |
| ---                | ---                     | ---      | ---                                                                                                                                                             |
| **ASTEdge**        | SourceID                | string   | The SHA-256 hash of the calling/importing node.                                                                                                                 |
| ---                | ---                     | ---      | ---                                                                                                                                                             |
| **ASTEdge**        | TargetID                | string   | The SHA-256 hash of the called/imported node.                                                                                                                   |
| ---                | ---                     | ---      | ---                                                                                                                                                             |
| **ASTEdge**        | EdgeType                | uint8    | An enumeration (Calls, Imports, Implements) stored as a single byte.                                                                                            |
| ---                | ---                     | ---      | ---                                                                                                                                                             |

## Semantic Embedding Engine Implementation

Structural extraction maps the topography of the codebase, but it remains blind to semantic intent. To empower the LLM to search by conceptual meaning rather than exact keywords, the daemon integrates a local semantic embedding engine to vectorize the ContentSum of the extracted nodes.

### ONNX Runtime Integration Strategies

The designated semantic model is all-MiniLM-L6-v2, selected for its proven efficacy in generating high-quality sentence embeddings (384-dimensional vectors) while maintaining an exceptionally small architectural footprint.

Deploying this model typically requires a heavy Python environment, a scenario strictly prohibited by the architecture's constraints. The solution is executing it via the ONNX Runtime. To bypass CGO entirely, the daemon utilizes github.com/shota3506/onnxruntime-purego.<sup>5</sup> This library uses pure-Go bindings (ebitengine/purego) to dynamically load the ONNX runtime library at execution, enabling cross-platform machine learning inference in Go without a C compiler.<sup>5</sup>

### Model Quantization and Binary Embedding

The standard all-MiniLM-L6-v2 model in FP32 format uses roughly 90 MB. To aggressively curtail memory consumption, the ONNX model undergoes 8-bit integer (INT8) quantization. This shrinks the model layer size down to roughly 21-25 MB, reducing the overall memory requirements by over 75% with minimal accuracy loss.

To achieve the "single binary" deployment goal, the quantized .onnx file and its tokenizer are embedded directly into the Go binary using the //go:embed directive. During boot, the binary passes the embedded byte array directly into memory to instantiate the ONNX Inference Session, avoiding disk extraction overhead.

## Relational and Vector Storage Layer Architecture

The persistence of the structural and semantic state is orchestrated by an embedded SQLite engine, specifically augmented with the sqlite-vec extension for vector search capabilities.<sup>6</sup>

### Schema Design and sqlite-vec Integration

The Go implementation utilizes the github.com/asg017/sqlite-vec-go-bindings/cgo package (or its WebAssembly equivalent ncruces wrapper if strict CGO avoidance is mandated). Invoking the sqlite_vec.Auto() initialization function registers the vector instruction set to the active database connection.<sup>2</sup> The 384-dimensional vectors output by the ONNX model are converted to compact BLOBs using the sqlite_vec.SerializeFloat32() function before insertion.<sup>2</sup>

The schema is partitioned to serve hybrid queries efficiently:

| **Table Definition** | **Architectural Purpose**                                                                                                                                                                                 |
| -------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **nodes**            | Stores structural metadata (SHA-256 id, file_path, symbol_name, node_type, start_byte, end_byte).                                                                                                         |
| ---                  | ---                                                                                                                                                                                                       |
| **edges**            | Stores the relational mapping (source_id, target_id, edge_type). Enforces FOREIGN KEY constraints.                                                                                                        |
| ---                  | ---                                                                                                                                                                                                       |
| **nodes_fts**        | Virtual table via FTS5 for rapid lexical keyword matching (BM25 algorithm) against symbol_name and summaries.                                                                                             |
| ---                  | ---                                                                                                                                                                                                       |
| **node_embeddings**  | Virtual table utilizing the vec0 extension, mapping node_id to its serialized 384-dimensional embedding. Configured to use distance_metric=cosine for optimal semantic distance calculations.<sup>6</sup> |
| ---                  | ---                                                                                                                                                                                                       |

### Hybrid Search Mechanics and Reciprocal Rank Fusion

The context MCP tool relies on a dual-path hybrid search:

- **Lexical Execution:** A MATCH query against the nodes_fts virtual table scores keyword relevance.
- **Semantic Execution:** The ONNX vector queries the node_embeddings virtual table using a KNN search (e.g., WHERE embedding MATCH? AND k = 10).<sup>6</sup>

The results are mathematically unified using Reciprocal Rank Fusion (RRF), ensuring that the LLM is provided with file and function summaries that are both conceptually aligned and syntactically relevant.

## Graph-Theoretic Context Resolution

To understand the blast radius of structural modifications, the daemon constructs an in-memory directed graph using the gonum/graph library based on the SQLite edges table.

### Blast Radius Analysis via Breadth-First Search (BFS)

The impact MCP tool calculates the "blast radius" by tracing downstream dependents. It executes a Breadth-First Search (BFS) using Gonum primitives. The algorithm maintains a strict queue for node exploration and a visited hash map (map\[int\]bool) to detect and terminate cyclic dependencies safely. The search depth is capped at ![](data:image/png;base64,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) to prevent returning the entire repository as a dependency.

### Structural Prioritization via Personalized PageRank

To prioritize structurally significant nodes within hybrid search results, the daemon leverages the PageRankSparse function from gonum.org/v1/gonum/graph/network.<sup>8</sup> By applying a variation known as _Personalized PageRank_ (PPR), the random walk "teleportation" vector is dynamically seeded to the files the developer is currently editing. This mathematically biases the structural importance score toward the developer's immediate working context, filtering out globally ubiquitous utility methods.

## Model Context Protocol (MCP) Interface Design

The operational intelligence of the daemon is exposed via the Model Context Protocol (MCP). The daemon uses the github.com/metoro-io/mcp-golang SDK to implement the server natively in Go.

### Transport Architecture: Standard I/O

For a local, single-developer workflow, the daemon utilizes the stdio transport mechanism. The LLM client (e.g., Claude Desktop or Cursor) spawns the Go daemon as a subprocess. The server reads JSON-RPC requests from stdin and writes serialized JSON-RPC responses to stdout. All internal diagnostic logging is strictly routed to stderr to prevent JSON-RPC message corruption.<sup>9</sup> Implementation in Go via the SDK is initialized as:

Go

server := mcp_golang.NewServer(stdio.NewStdioServerTransport())

### MCP Toolset Specifications

The daemon registers five highly specialized tools via the MCP SDK:

| **Tool Identifier** | **Core Functionality**       | **Execution Mechanism**                                                                                                                            |
| ------------------- | ---------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------- |
| **context**         | Discovers relevant code.     | Integrates SQLite FTS5 (BM25 lexical matching), sqlite-vec (semantic cosine matching), and Gonum Personalized PageRank to return ranked summaries. |
| ---                 | ---                          | ---                                                                                                                                                |
| **impact**          | Analyzes blast radius.       | Executes a BFS algorithm on the Gonum in-memory graph. Explores all downstream caller/dependent edges up to \$N\$ depth.                           |
| ---                 | ---                          | ---                                                                                                                                                |
| **read_symbol**     | Retrieves exact source code. | Queries the nodes SQLite table for exact start_byte and end_byte offsets. Reads only specific bytes directly from the disk.                        |
| ---                 | ---                          | ---                                                                                                                                                |
| **query**           | Diagnostic database tool.    | Accepts a raw SQL query string and executes it directly against the SQLite database containing the structural mapping.                             |
| ---                 | ---                          | ---                                                                                                                                                |
| **index**           | Manages daemon state.        | Exposes manual overrides to trigger a full Tree-sitter AST walk of the repository.                                                                 |
| ---                 | ---                          | ---                                                                                                                                                |

## Future Trajectories: The "Cold Start" Solver

While the current architecture successfully maps _how_ the codebase operates, future iterations of the daemon will ingest local Git metadata (via pure-Go go-git). By concatenating historical PR descriptions and commit messages with the AST nodes before passing them through the ONNX semantic embedder, the daemon will bridge the gap between mechanical structure and human intent. This will provide LLM agents with historical business logic alongside byte-accurate source code.

## References & Libraries

- **Tree-Sitter Parsing:** gotreesitter (pure Go implementation, eliminates CGO) - github.com/odvcencio/gotreesitter.<sup>1</sup>
- **Vector Database:** sqlite-vec (Go bindings via sqlite_vec.Auto()) - github.com/asg017/sqlite-vec-go-bindings.<sup>2</sup>
- **Semantic Engine (ONNX):** onnxruntime-purego (Pure Go bindings without CGO) - github.com/shota3506/onnxruntime-purego.<sup>5</sup>
- **ONNX Model Details:** Sentence Transformers memory footprint and quantization (all-MiniLM-L6-v2).
- **Filesystem Events:** fsbroker (Event debouncing and deduplication) - github.com/helshabini/fsbroker.<sup>3</sup>
- **Exclusion Logic:** go-gitignore (Matching engine) - github.com/crackcomm/go-gitignore.<sup>4</sup>
- **Graph Engine:** gonum/graph (PageRankSparse and Network algorithms) - gonum.org/v1/gonum/graph/network.<sup>8</sup>
- **MCP Server SDK:** mcp-golang (Go server implementation with stdio support) - github.com/metoro-io/mcp-golang.

#### Works cited

- odvcencio/gotreesitter: Pure Go tree-sitter runtime - GitHub, accessed March 30, 2026, <https://github.com/odvcencio/gotreesitter>
- Using sqlite-vec in Go | sqlite-vec - Alex Garcia, accessed March 30, 2026, <https://alexgarcia.xyz/sqlite-vec/go.html>
- FSBroker is a Go library which aims to broker, group, dedup, and filter FSNotify events. - GitHub, accessed March 30, 2026, <https://github.com/helshabini/fsbroker>
- ignore package - github.com/crackcomm/go-gitignore - Go Packages, accessed March 30, 2026, <https://pkg.go.dev/github.com/crackcomm/go-gitignore>
- shota3506/onnxruntime-purego: A Pure Go binding for ONNX Runtime using ebitengine ... - GitHub, accessed March 30, 2026, <https://github.com/shota3506/onnxruntime-purego>
- How sqlite-vec Works for Storing and Querying Vector Embeddings | by Stephen Collins, accessed March 30, 2026, <https://medium.com/@stephenc211/how-sqlite-vec-works-for-storing-and-querying-vector-embeddings-165adeeeceea>
- KNN queries | sqlite-vec - Alex Garcia, accessed March 30, 2026, <https://alexgarcia.xyz/sqlite-vec/features/knn.html>
- network package - gonum.org/v1/gonum/graph/network - Go Packages, accessed March 30, 2026, <https://pkg.go.dev/gonum.org/v1/gonum/graph/network>
- Creating an MCP Server Using Go - DEV Community, accessed March 30, 2026, <https://dev.to/eminetto/creating-an-mcp-server-using-go-3foe>