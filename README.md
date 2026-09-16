# ⚡ Phosphor

**Phosphor** is an intelligent, resilient, terminal-first LLM gateway and routing reverse proxy written in pure Go. It exposes an OpenAI-compatible API surface, seamlessly routes requests across providers using cost-, latency-, and session-aware arbitration, intercepts failures with an automatic circuit-breaking failover engine, and persists telemetry in a zero-CGo SQLite database.

```
                  +----------------------------------------------+
                  |         Client / SDK / Agent / curl          |
                  +----------------------------------------------+
                                         |
                       POST /v1/chat/completions
                                         v
   +-------------------------------------------------------------------------+
   |                            PHOSPHOR GATEWAY                             |
   |                                                                         |
   |  +---------------------+  +--------------------+  +------------------+  |
   |  |   Streaming SSE     |  |   Smart Arbiter    |  | Circuit Breakers |  |
   |  |   Flush Pipeline    |  | Cost, TTFT, Sticky |  | State Machine    |  |
   |  +---------------------+  +--------------------+  +------------------+  |
   |                                     |                                   |
   |                    +----------------+---------------+                   |
   |                    |                                |                   |
   |                    v                                v                   |
   |           +-----------------+              +-----------------+          |
   |           | Primary Target  |              | Failover Target |          |
   |           | OpenAI / Groq   | --[429/5xx]->| Cohere/Anthropic|          |
   |           +-----------------+              +-----------------+          |
   |                                                                         |
   |        Telemetry: modernc.org/sqlite (Zero CGo Spend & Traces)          |
   +-------------------------------------------------------------------------+
                                         |
                           phosphor stats / logs / ping
                                         v
                    +-----------------------------------------+
                    |        Lipgloss Terminal Dashboard      |
                    +-----------------------------------------+
```

---

## Key Features

* **OpenAI-Compatible Inbound API**: Transparent drop-in replacement for OpenAI SDKs, LangChain, LlamaIndex, Cursor, Continue, or `curl`. Serves `POST /v1/chat/completions`, `GET /v1/models`, `GET /health`, `GET /ready`, `GET /metrics`, and `GET /openapi.json`.
* **Zero-Allocation SSE Streaming Pipeline**: True low-latency chunk streaming with `sync.Pool` buffer reuse, immediate client-disconnect cancellation to abort upstream token generation, configurable streaming idle read timeouts, and mid-stream error surfacing.
* **Smart Multi-Strategy Routing Engine**:
  * `priority`: Sequential priority list with automatic cascading.
  * `weighted-round-robin`: Smooth traffic balancing across equivalent providers according to configured weights.
  * `least-cost`: Computes prompt tokens heuristic and dynamically routes to the cheapest upstream provider.
  * `lowest-latency`: Routes to the lowest latency provider using a rolling Exponential Moving Average (EMA) of Time-To-First-Token (TTFT).
  * `sticky-session`: Deterministically routes conversational sessions (`X-Session-ID` or `user`) to the same provider using consistent hashing, maximizing KV cache affinity and reducing cold starts.
  * `composite`: Multi-objective scoring balancing cost and latency ($S = w_{\text{cost}} \cdot \hat{C} + w_{\text{lat}} \cdot \hat{L}$).
  * **Header Routing Overrides**: Override model routing per-request using `X-Phosphor-Model` or `X-Routing-Model`.
  * **Model Capability Matching**: Automatically validates requests requiring Vision, Tools/Function Calling, or JSON mode against target provider capabilities.
  * **Fallback Model Groups**: Automatic cross-family failover (e.g. failing over from `gpt-4o` to `claude-3-5-sonnet` or local `llama3.2`).
* **Resilient Failover & Circuit Breakers**:
  * Automatically cascades on transient faults (HTTP 429, 5xx, timeouts, connection resets) with Exponential Backoff and Full Jitter ($t = \text{random}(0, \min(M, B \times 2^{\text{attempt}}))$).
  * **HTTP 429 `Retry-After` Header Parsing**: Automatically pauses backoff sleep according to upstream seconds or RFC1123 date headers.
  * **Retry Budget Tracker**: Dynamic token-bucket retry budget prevents cascading upstream overloads (`routing.retry_budget_ratio`).
  * Deterministic client errors (400 Bad Request, 422) fail fast immediately without tripping breakers.
  * Isolated Circuit Breakers (`Closed` $\rightarrow$ `Open` $\rightarrow$ `Half-Open`) with background health probers for proactive recovery.
* **Security & Access Control**:
  * **IP Whitelist & CIDR Filtering**: Restrict gateway traffic to trusted client IP addresses and subnets (`security.allowed_ips`, `security.blocked_ips`).
  * **Prompt Injection & Jailbreak Guard**: Pre-flight text scanner detecting adversarial prompts and privilege escalation attempts (`security.enable_prompt_guard`).
  * **Prompt Length & Token Limits**: Protects downstream inference engines from memory exhaustion (`security.max_prompt_chars`, `security.max_prompt_tokens`).
  * Constant-time Bearer and `x-api-key` authentication supporting plaintext and SHA-256 hashed keys (`phosphor hash-key`).
  * Per-client token-bucket rate limiting with HTTP 429 and `Retry-After` headers.
  * Tenant spend quotas and budget warnings (`X-Budget-Warning`).
  * Automatic secret and API key masking/redaction in all logs, error messages, and database traces.
  * Provider concurrency semaphores to prevent upstream connection saturation.
* **Caching & Compression Performance**:
  * **HTTP Response Compression (gzip)**: Transparent gzip compression middleware with buffer pooling for non-streaming responses.
  * **ETag Caching & HTTP 304 Not Modified**: Computes deterministic SHA-256 ETags and validates `If-None-Match` on cache hits, avoiding re-transmission.
  * Multi-tier LRU in-memory and SQLite persistent response cache with streaming replay support.
* **High-Throughput Asynchronous SQLite Telemetry**:
  * Powered by pure-Go `modernc.org/sqlite` (100% CGo-free).
  * In-memory bounded event queue with background batch worker flushing SQLite transactions in WAL mode.
  * **Graceful Degradation Mode**: Automatically falls back to in-memory non-blocking mode if database disk writes encounter persistent I/O errors.
  * **Online Database Backups & Maintenance**: Atomic live snapshot creation (`phosphor db backup`) and page defragmentation (`phosphor db vacuum`).
* **Production Observability & Monitoring**:
  * Prometheus `/metrics` endpoint exposing request counts, token counters, estimated spend, provider EMA latencies, latency percentiles (`p50`, `p90`, `p99`), and queue depth.
  * Kubernetes-ready `/ready` readiness probe verifying database connectivity and provider availability.
* **Rich Terminal-First CLI**:
  * `phosphor ping`: Healthcheck gateway latency, readiness, and provider status.
  * `phosphor stats`: Styled terminal dashboard displaying spend cards, token usage, and provider health.
  * `phosphor logs`: Real-time request log inspection with `--client`, `--model`, and `--failovers` filters.
  * `phosphor db backup / vacuum`: Database snapshot creation and defragmentation.
  * `phosphor config check / view`: Configuration schema validation and credential-redacted inspector.
  * `phosphor cache stats / clear`: Cache capacity and eviction management.
  * `phosphor budgets`: Tenant quota inspection.
  * `phosphor openapi`: Export OpenAPI v3 JSON specification.

---

## Supported Upstream Providers

| Provider | Type | Supported Models | Capabilities | Protocol |
| :--- | :--- | :--- | :--- | :--- |
| **OpenAI** | `openai` | `gpt-4o`, `gpt-4o-mini`, `o1`, `o3-mini` | `vision`, `tools`, `json` | REST / SSE |
| **Groq** | `groq` | `llama-3.3-70b-versatile`, `llama-3.1-8b-instant` | `tools`, `json` | OpenAI-compatible REST / SSE |
| **Anthropic** | `anthropic` | `claude-3-5-sonnet-20241022`, `claude-3-5-haiku-20241022` | `vision`, `tools`, `json` | Claude Messages API translation |
| **Gemini** | `gemini` | `gemini-2.0-flash`, `gemini-1.5-pro` | `vision`, `tools`, `json` | Google OpenAI-compat REST / SSE |
| **Cohere** | `cohere` | `command-r-plus`, `command-r`, `command-light` | `tools`, `json` | Cohere v2 Chat API translation |
| **Ollama** | `ollama` | `llama3.2`, `mistral`, `deepseek-r1` | `json` | Local daemon (`localhost:11434`) |

---

## API Endpoints Reference

| Endpoint | Method | Authentication | Description |
| :--- | :--- | :--- | :--- |
| `/v1/chat/completions` | `POST` | Optional / Required | Main chat completions API. Supports sync JSON and streaming SSE (`"stream": true`), ETag 304, and gzip. |
| `/v1/models` | `GET` | Optional / Required | OpenAI-compatible catalog listing all virtual routing groups and underlying provider models. |
| `/health` | `GET` | None | Fast liveness probe returning `{"status": "ok"}`. |
| `/ready` | `GET` | None | Kubernetes readiness probe verifying database connectivity and provider states (`200 OK` or `503 Service Unavailable`). |
| `/metrics` | `GET` | None | Prometheus-compatible metrics endpoint scraping request counters, latencies, percentiles (`p50`, `p90`, `p99`), tokens, and spend. |
| `/openapi.json` | `GET` | None | OpenAPI v3 specification documentation endpoint. |
| `/v1/admin/cache/stats` | `GET` | Admin Key | Cache hit/miss ratio, capacity, and size metrics. |
| `/v1/admin/cache/clear` | `POST` | Admin Key | Purge all memory and persistent cache entries. |
| `/v1/admin/db/backup` | `POST` | Admin Key | Create an online atomic SQLite database backup snapshot. |
| `/v1/admin/db/vacuum` | `POST` | Admin Key | Trigger online SQLite database defragmentation and page reclamation. |
| `/v1/admin/budgets` | `GET` | Admin Key | Query tenant spend limits and quota utilization. |

---

## Installation & Build

Requires Go 1.22+.

```bash
# Clone the repository
git clone https://github.com/Sriram-Nambiar/Phosphor.git
cd Phosphor

# Build binary (pure Go, zero CGo required)
go build -o phosphor ./cmd/phosphor
```

---

## Quickstart

### 1. Configure Providers
Copy the provided template and add your API keys:

```bash
cp config.example.yaml config.yaml
```

Example `config.yaml`:

```yaml
server:
  host: "127.0.0.1"
  port: 8080
  read_timeout: 60s
  write_timeout: 120s

database:
  path: "~/.phosphor/phosphor.db"

routing:
  default_strategy: "priority" # "priority", "least-cost", "lowest-latency", "sticky-session", "weighted-round-robin"
  timeout_seconds: 30
  retry_budget_ratio: 0.2

security:
  enable_prompt_guard: true
  block_threshold: 0.7
  allowed_ips:
    - "127.0.0.1"
    - "10.0.0.0/8"

cache:
  enabled: true
  capacity: 1000
  ttl: 5m

providers:
  - name: "openai"
    type: "openai"
    base_url: "https://api.openai.com/v1"
    api_key: "${OPENAI_API_KEY}"
    enabled: true
    models: ["gpt-4o", "gpt-4o-mini"]
    cost:
      prompt_cost_per_1m: 2.50
      completion_cost_per_1m: 10.00

  - name: "cohere"
    type: "cohere"
    base_url: "https://api.cohere.com/v2"
    api_key: "${COHERE_API_KEY}"
    enabled: true
    models: ["command-r-plus"]
    cost:
      prompt_cost_per_1m: 2.50
      completion_cost_per_1m: 10.00

  - name: "groq"
    type: "groq"
    base_url: "https://api.groq.com/openai/v1"
    api_key: "${GROQ_API_KEY}"
    enabled: true
    models: ["llama-3.3-70b-versatile"]
    cost:
      prompt_cost_per_1m: 0.59
      completion_cost_per_1m: 0.79

models:
  default:
    strategy: "sticky-session"
    targets:
      - provider: "openai"
        model: "gpt-4o-mini"
      - provider: "groq"
        model: "llama-3.3-70b-versatile"
```

### 2. Validate Configuration Health
```bash
./phosphor config check config.yaml
```

### 3. Start the Gateway Daemon
```bash
./phosphor start
```

### 4. Send Completions

**Standard Non-Streaming (with ETag & Compression):**
```bash
curl -i http://127.0.0.1:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Accept-Encoding: gzip" \
  -H "X-Session-ID: session-12345" \
  -d '{
    "model": "default",
    "messages": [{"role": "user", "content": "Explain circuit breakers in 2 sentences."}]
  }'
```

**Subsequent Request with ETag (Returns 304 Not Modified):**
```bash
curl -i http://127.0.0.1:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "If-None-Match: \"<ETAG_FROM_PREVIOUS_RESPONSE>\"" \
  -d '{
    "model": "default",
    "messages": [{"role": "user", "content": "Explain circuit breakers in 2 sentences."}]
  }'
```

**Streaming SSE:**
```bash
curl -N http://127.0.0.1:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "default",
    "stream": true,
    "messages": [{"role": "user", "content": "Count from 1 to 5."}]
  }'
```

---

## Docker & Container Deployment

Phosphor provides a production-ready, multi-stage Alpine Dockerfile that requires 0 CGo dependencies:

```bash
# Build Docker image
docker build -t phosphor:latest .

# Run with Docker Compose
docker-compose up -d

# Check gateway health
docker-compose exec phosphor phosphor ping
```

---

## CLI Reference

### `phosphor ping`
Queries gateway health, readiness, and round-trip latency:
```bash
./phosphor ping --count 3 --interval 1s
```

### `phosphor stats`
Displays a styled aggregate spend dashboard, token breakdown, and rolling EMA TTFT per provider:
```bash
./phosphor stats
```

### `phosphor logs`
Inspects real-time request logs and visualizes failover cascades:
```bash
./phosphor logs --client client-a --limit 15
./phosphor logs --failovers
```

### `phosphor db backup` & `vacuum`
Performs live atomic backups and SQLite database page defragmentation:
```bash
./phosphor db backup ./backups/snapshot.db
./phosphor db vacuum
```

### `phosphor config check` & `view`
Validates syntax and prints resolved configurations with redacted secrets:
```bash
./phosphor config check ./config.yaml
./phosphor config view
```

### `phosphor cache stats` & `clear`
Inspects cache performance and purges stale entries:
```bash
./phosphor cache stats
./phosphor cache clear
```

### `phosphor hash-key`
Generates salted SHA-256 API key hashes for configuration security:
```bash
./phosphor hash-key sk-my-secret-key
```

---

## Testing

Run unit tests, integration tests, and race-detector stress tests:

```bash
# Run all unit tests with race detection
go test -v -race ./...

# Run concurrent failover and gateway stress tests
go test -v -race ./test -run TestGateway_ConcurrentStressAndFailover
```

---

## License
MIT License
