# Gemini Codebase Indexer With Persist Memory Extension

A model-agnostic Gemini CLI extension and MCP server in **Go** providing local codebase indexing and semantic search. It uses **DuckDB** for metadata and a quantized **TurboQuant** vector index for 12x-compressed, 3000x-accelerated similarity search.

---

## 💡 Motivation & Tool Selection Guidelines

Traditional `grep` searches across large codebases are highly token-inefficient, loading unnecessary matches and noise into the agent's context window—slowing down execution and driving up API costs. This extension provides semantic local indexing to help agents locate relevant code **faster and cheaper**.

For the most efficient workflow:
*   Use **`search_memory`** (Semantic Search) to explore code conceptually (e.g., finding where "storage cleanup" is handled or searching for an architectural concept).
*   Use **`search_call_graph`** to trace dependencies and understand function/resource execution flows.
*   Use standard **`grep`** only when you "Know the exact identifier" (e.g., a specific variable name, system property, or unique error string) or strictly "Need ALL matches".

---

## ✨ Key Features

*   **Merkle Tree Incremental Sync:** Computes directory tree diffs to index/re-embed only added or modified files (supporting `.go`, `.tf`, and `.yaml`/`.yml`).
*   **Privacy-Preserving Vector Storage:** No code is stored in the database. Only metadata headers are saved; raw code is read directly from local disk on demand during search.
*   **AST Call & Dependency Graph:** Extracts call nodes and edges incrementally into DuckDB, allowing fast traversal and ASCII call-tree generation.

> ⚠️ **Note:** Currently, the codebase indexer and call graph builder support indexing `.go`, `.tf`, and `.yaml` / `.yml` files.

---

## 🛠 Exposed MCP Tools

1.  **`search_memory`**: Semantic search across indexed workspace code blocks.
2.  **`search_call_graph`**: Explores bidirectional call chains (caller/callee)

---

## 🚀 Quick Start

### 1. Build and Install
```bash
make install
```

### 2. Index a Codebase
```bash
make index DIR=/path/to/your/codebase
```

### 3. Run Tests
```bash
make test         # Run unit tests
make test-all     # Run all tests & database self-checks
```

---

## ⚙ Configuration

Configure via environment variables:
*   `LITELLM_BASE_URL`: API base URL (Default: `http://localhost:36253/v1`)
*   `LITELLM_EMBEDDING_MODEL`: Embedding model (Default: `gemini-embedding-001`)
*   `LITELLM_CHAT_MODEL`: Chat model (Default: `gpt-5`)

---

## 📐 System Architecture

```mermaid
graph TD
    %% Entrypoints
    subgraph Client ["Client / Agent Entrypoints"]
        cli[Gemini CLI]
        idx_cli[Indexer CLI]
    end

    %% User's Environment
    subgraph Workspace ["User's Local Workspace (On Disk)"]
        code_files[User's Source Code<br/>.go, .tf, .yaml]
    end

    %% Extension
    subgraph Extension ["agent-mem Extension"]
        subgraph MCP ["Exposed MCP Server"]
            mcp_srv[MCP Server<br/>cmd/server]
        end

        subgraph Core ["Core Engine"]
            merkle[Merkle Sync<br/>internal/merkle]
            splitter[Splitter<br/>internal/splitter]
            llm[LLM Client<br/>internal/llm]
            db[Metadata DB<br/>internal/db]
            tq[TurboQuant Index<br/>internal/turboquant]
            callgraph[Call Graph<br/>internal/callgraph]
        end
    end

    %% Providers
    subgraph Provider ["Embedding Provider"]
        litellm[LiteLLM API]
    end

    %% Storage
    subgraph Storage ["Persistent Storage (~/.gemini/)"]
        duckdb_file[(agent-mem.db<br/>DuckDB Metadata & Call Graph<br/>NO CODE STORED)]
        tqv_file[(agent-mem.tqv<br/>Quantized Vectors)]
    end

    %% Indexing Flow
    idx_cli -.->|Index code| merkle
    mcp_srv -.->|Periodically refresh the indexed code| merkle

    code_files -.->|Scan Directory| merkle
    merkle -->|2. Split Code Files| splitter
    merkle -->|3. Get Embeddings| llm
    llm -->|4. REST API| litellm
    merkle -->|5. Save Metadata Only| db
    merkle -->|6. Store Vectors| tq
    merkle ==>|7. Incremental Call Graph Parse| callgraph
    callgraph ==>|8. Save Nodes & Edges| db

    %% MCP Query Flow
    cli -->|1. Call search_memory| mcp_srv
    mcp_srv -->|2. Index.Search| tq
    mcp_srv -->|3. Fetch Metadata Only| db
    mcp_srv ==>|4. DYNAMICALLY READ CODE LINES<br/>FOR PRIVACY| code_files
    mcp_srv -->|5. Return Combined Context| cli
    cli ==>|Call search_call_graph| mcp_srv

    %% Persistence
    db -->|Read/Write SQL| duckdb_file
    tq -->|Load/Save Index| tqv_file
```

---

## 📊 TurboQuant Vector Compression Benchmark

> See [detailed](https://github.com/datnguyenzzz/agent-context/blob/main/scripts/benchmark_compression_test.go) 

```
================================================================================
        📊  TURBOQUANT VECTOR COMPRESSION BENCHMARK SUITE  📊                 
================================================================================

📁 Targets: Aggregated Index (across 11 codebases)
   • Scanned Files: 18_017 | Total Semantic Chunks: 141_210 | Dimensions: 1536
   • Total Lines of Code (LOC): 3_436_130 | DuckDB Metadata Size: 0.76 MiB
  -------------------------------------------------------------------------------- 
   │ Data Footprint Type            │ Footprint Size │ Comp. Ratio │ Savings    │
   ├────────────────────────────────┼────────────────┼─────────────┼────────────┤
   │ [1] Standard Float32[] RAM     │     827.40 MiB │      1.0x   │     0.0%   │
   │ [2] TurboQuant In-Memory Map   │     105.85 MiB │      7.8x   │    87.2%   │
   │ [3] TurboQuant On-Disk .tqv    │      56.81 MiB │     14.6x   │    93.1%   │
   └────────────────────────────────┴────────────────┴─────────────┴────────────┘

   📈 Visual Storage Footprint Comparison (Bar Scale):

   Standard Float32[] RAM   : [████████████████████████████████████████] (827.40 MiB)
   TurboQuant In-Memory Map : [█████░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░] (105.85 MiB) 
   TurboQuant On-Disk .tqv  : [██░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░] (56.81 MiB)

================================================================================
```
