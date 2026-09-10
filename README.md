# ⚡ Phosphor

**Phosphor** is an intelligent, resilient, terminal-first LLM gateway and routing reverse proxy written in pure Go. It exposes an OpenAI-compatible API surface, seamlessly routes requests across providers using cost- and latency-aware arbitration, intercepts failures with an automatic circuit-breaking failover engine, and persists telemetry in a zero-CGo SQLite database.

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
   |  |   Flush Pipeline    |  |  Cost & TTFT EMA   |  | State Machine    |  |
   |  +---------------------+  +--------------------+  +------------------+  |
   |                                     |                                   |
   |                    +----------------+---------------+                   |
   |                    |                                |                   |
   |                    v                                v                   |
   |           +-----------------+              +-----------------+          |
   |           | Primary (e.g.)  |              | Failover (e.g.) |          |
   |           |  OpenAI / Groq  | --[429/5xx]->| Anthropic/Ollama|          |
   |           +-----------------+              +-----------------+          |
   |                                                                         |
   |        Telemetry: modernc.org/sqlite (Zero CGo Spend & Traces)          |
   +-------------------------------------------------------------------------+
                                         |
                                 phosphor stats / logs
                                         v
                    +-----------------------------------------+
                    |        Lipgloss Terminal Dashboard      |
                    +-----------------------------------------+
```

---

## Key Features
 
* **OpenAI-Compatible Inbound API**: Transparent drop-in replacement for OpenAI SDKs, LangChain, LlamaIndex, Cursor, Continue, or `curl`. Serves `POST /v1/chat/completions`, `GET /v1/models`, `GET /health`, `GET /ready`, and `GET /metrics`.
* **Zero-Allocation SSE Streaming Pipeline**: True low-latency chunk streaming with `sync.Pool` buffer reuse, immediate client-disconnect cancellation to abort upstream token generation, configurable streaming idle read timeouts, and mid-stream error surfacing.
* **Smart Multi-Strategy Routing Engine**:
  * `priority`: Sequential priority list with automatic cascading.
  * `weighted-round-robin`: Smooth traffic balancing across equivalent providers according to configured weights.
  * `least-cost`: Computes prompt tokens heuristic and dynamically routes to the cheapest upstream provider.
  * `lowest-latency`: Routes to the lowest latency provider using a rolling Exponential Moving Average (EMA) of Time-To-First-Token (TTFT).
  * `composite`: Multi-objective scoring balancing cost and latency ($S = w_{\text{cost}} \cdot \hat{C} + w_{\text{lat}} \cdot \hat{L}$).
  * **Model Capability Matching**: Automatically validates requests requiring Vision, Tools/Function Calling, or JSON mode against target provider capabilities.
  * **Fallback Model Groups**: Automatic cross-family failover (e.g. failing over from `gpt-4o` to `claude-3-5-sonnet` or local `llama3.2`).
* **Resilient Failover & Circuit Breakers**:
  * Automatically cascades on transient faults (HTTP 429, 5xx, timeouts, connection resets) with Exponential Backoff and Full Jitter ($t = \text{random}(0, \min(M, B \times 2^{\text{attempt}}))$).
  * Deterministic client errors (400 Bad Request, 422) fail fast immediately without tripping breakers.
  * Isolated Circuit Breakers (`Closed` $\rightarrow$ `Open` $\rightarrow$ `Half-Open`) with background health probers for proactive recovery.
* **Security & Access Control**:
  * Constant-time Bearer and `x-api-key` authentication supporting plaintext and SHA-256 hashed keys (`phosphor hash-key`).
  * Per-client token-bucket rate limiting with HTTP 429 and `Retry-After` headers.
  * Per-key model access permissions and model allowlists.
  * Automatic secret and API key masking/redaction in all logs, error messages, and database traces.
  * Provider concurrency semaphores to prevent upstream connection saturation.
* **High-Throughput Asynchronous SQLite Telemetry**:
  * Powered by pure-Go `modernc.org/sqlite` (100% CGo-free).
  * In-memory bounded event queue (4096 capacity) with background batch worker flushing SQLite transactions in WAL mode.
  * Read-your-own-writes consistency and zero-loss queue draining during graceful server shutdown.
* **Production Observability & Monitoring**:
  * Prometheus `/metrics` endpoint exposing gateway requests, token counters, estimated costs, provider EMA latencies, circuit breaker states, and queue depth.
  * Kubernetes-ready `/ready` readiness probe verifying database connectivity and provider availability.
* **Terminal-First Dashboard**:
  * `phosphor stats`: Styled terminal dashboard displaying spend cards, token usage, and provider health using `charmbracelet/lipgloss`.
  * `phosphor logs`: Real-time request log inspection and visual failover cascade traces (`OpenAI ➔ Groq`).
  * `phosphor hash-key`: Secure CLI utility for generating salted SHA-256 API key hashes for configuration.

---

## Supported Upstream Providers

| Provider | Type | Supported Models | Capabilities | Protocol |
| :--- | :--- | :--- | :--- | :--- |
| **OpenAI** | `openai` | `gpt-4o`, `gpt-4o-mini`, `o1`, `o3-mini` | `vision`, `tools`, `json` | REST / SSE |
| **Groq** | `groq` | `llama-3.3-70b-versatile`, `llama-3.1-8b-instant` | `tools`, `json` | OpenAI-compatible REST / SSE |
| **Anthropic** | `anthropic` | `claude-3-5-sonnet-20241022`, `claude-3-5-haiku-20241022` | `vision`, `tools`, `json` | Claude Messages API translation |
| **Gemini** | `gemini` | `gemini-2.0-flash`, `gemini-1.5-pro` | `vision`, `tools`, `json` | Google OpenAI-compat REST / SSE |
| **Ollama** | `ollama` | `llama3.2`, `mistral`, `deepseek-r1` | `json` | Local daemon (`localhost:11434`) |

---

## API Endpoints Reference

| Endpoint | Method | Authentication | Description |
| :--- | :--- | :--- | :--- |
| `/v1/chat/completions` | `POST` | Optional / Required | Main chat completions API. Supports sync JSON and streaming SSE (`"stream": true`). |
| `/v1/models` | `GET` | Optional / Required | OpenAI-compatible catalog listing all virtual routing groups and underlying provider models. |
| `/health` | `GET` | None | Fast liveness probe returning `{"status": "ok"}`. |
| `/ready` | `GET` | None | Kubernetes readiness probe verifying database connectivity and provider states (`200 OK` or `503 Service Unavailable`). |
| `/metrics` | `GET` | None | Prometheus-compatible metrics endpoint scraping request counters, latencies, tokens, spend, and queue depth. |

---

## Installation & Build

Requires Go 1.22+.

```bash
# Clone the repository
git clone https://github.com/Sriram-Nambiar/Phosphor.git
cd Phosphor

# Build binary
go build -o phosphor.exe ./cmd/phosphor
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
  default_strategy: "priority" # "priority", "least-cost", "lowest-latency"
  timeout_seconds: 30

circuit_breaker:
  failure_threshold: 3
  cooldown_seconds: 30

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

  - name: "groq"
    type: "groq"
    base_url: "https://api.groq.com/openai/v1"
    api_key: "${GROQ_API_KEY}"
    enabled: true
    models: ["llama-3.3-70b-versatile"]
    cost:
      prompt_cost_per_1m: 0.59
      completion_cost_per_1m: 0.79

  - name: "ollama"
    type: "ollama"
    base_url: "http://localhost:11434"
    enabled: true
    models: ["llama3.2"]
    cost:
      prompt_cost_per_1m: 0.0
      completion_cost_per_1m: 0.0

models:
  default:
    strategy: "priority"
    targets:
      - provider: "openai"
        model: "gpt-4o-mini"
      - provider: "groq"
        model: "llama-3.3-70b-versatile"
      - provider: "ollama"
        model: "llama3.2"

  fast:
    strategy: "lowest-latency"
    targets:
      - provider: "groq"
        model: "llama-3.3-70b-versatile"
      - provider: "openai"
        model: "gpt-4o-mini"

  cheapest:
    strategy: "least-cost"
    targets:
      - provider: "ollama"
        model: "llama3.2"
      - provider: "groq"
        model: "llama-3.3-70b-versatile"
      - provider: "openai"
        model: "gpt-4o-mini"
```

### 2. Start the Gateway Daemon
```bash
./phosphor start
```

### 3. Send Completions

**Standard Non-Streaming:**
```bash
curl http://127.0.0.1:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "fast",
    "messages": [{"role": "user", "content": "Explain circuit breakers in 2 sentences."}]
  }'
```

**Streaming SSE:**
```bash
curl -N http://127.0.0.1:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "cheapest",
    "stream": true,
    "messages": [{"role": "user", "content": "Count from 1 to 5."}]
  }'
```

---

## CLI Inspection Commands

### `phosphor stats`
Displays a styled aggregate spend dashboard, token breakdown, and rolling EMA TTFT per provider:

```
  📊 PHOSPHOR TELEMETRY DASHBOARD  
Database: ~/.phosphor/phosphor.db

╭──────────────────────╮ ╭──────────────────────╮ ╭──────────────────────╮
│ Total Spend          │ │ Total Requests       │ │ Total Tokens         │
│ $0.003410            │ │ 42                   │ │ 18,920               │
╰──────────────────────╯ ╰──────────────────────╯ ╰──────────────────────╯
╭──────────────────────╮ ╭──────────────────────╮ ╭──────────────────────╮
│ Total Failovers      │ │ Avg Latency          │ │ Avg TTFT             │
│ 3                    │ │ 284.2 ms             │ │ 94.8 ms              │
╰──────────────────────╯ ╰──────────────────────╯ ╰──────────────────────╯

Provider Metrics & Health:
PROVIDER         MODEL                    REQUESTS   FAILURES   EMA TTFT     EMA LATENCY 
groq             llama-3.3-70b-versatile  28         1          88.4 ms      210.2 ms    
openai           gpt-4o-mini              14         2          185.0 ms     412.5 ms    
```

### `phosphor logs`
Inspects real-time request logs and visualizes failover cascades:

```
  ⚡ RECENT FAILOVER TRACES  
TIMESTAMP            REQUEST ID       FAILOVER CASCADE                           LATENCY      REASON
19:42:10 10-Sep      chatcmpl-a918..  openai ➔ groq                              48.2 ms      upstream openai returned HTTP 429

  📋 RECENT GATEWAY REQUESTS  
TIME               MODEL REQ      ROUTED TO        PROVIDER     STATUS   TOKENS     COST       LAT / TTFT     TYPE
19:42:15           fast           llama-3.3-70b    groq         200      312        $0.00021   198/84ms       [SSE]
19:42:10           resilient      llama-3.3-70b    groq         200      450        $0.00032   240ms          [JSON]
```

Use `-f` or `--failovers` to filter exclusively for failover events:
```bash
./phosphor logs --failovers
```

---

## Architecture & Internals

### Circuit Breaker State Machine & Proactive Health Probing
Every upstream provider has an isolated circuit breaker managing transient error conditions (HTTP 429, HTTP 5xx, context deadlines, network timeouts):

```
       +------------------+
       |   CLOSED (OK)    | <-------------+
       +------------------+               |
                 |                        |
         consecutive errors >= threshold  | probe succeeds / request succeeds
                 |                        |
                 v                        |
       +------------------+               |
       |    OPEN (TRIP)   |               |
       +------------------+               |
                 |                        |
         cooldown expired                 |
                 v                        |
       +------------------+               |
       |    HALF-OPEN     | --------------+
       +------------------+
                 |
         probe fails
                 v
             (back to OPEN)
```

In addition to passive recovery upon request arrival, a background health checker runs active probes against tripped providers during their cooldown period, testing liveness proactively.

### Multi-Objective Routing & Candidate Scoring
Phosphor supports five scoring strategies configured globally or per-virtual model:

* **`priority`**: Evaluates targets in sequential order.
* **`weighted-round-robin`**: Distributes queries proportionally to configured weights (e.g., 7:3 between two providers).
* **`least-cost`**: Evaluates prompt tokens heuristic (~4 characters per token + framing overhead) and computes dollar cost:
  $$\text{Estimated Cost} = \frac{\text{PromptTokens}}{1,000,000} \times \text{Cost}_{\text{prompt}} + \frac{\text{EstCompletionTokens}}{1,000,000} \times \text{Cost}_{\text{completion}}$$
* **`lowest-latency`**: Compares rolling Exponential Moving Average (EMA) of Time-To-First-Token (TTFT):
  $$\text{EMA}_{\text{new}} = (\alpha \times \text{TTFT}_{\text{observed}}) + ((1 - \alpha) \times \text{EMA}_{\text{prev}})$$
* **`composite`**: Jointly optimizes for both cost and latency using min-max normalized candidate metrics:
  $$S = w_{\text{cost}} \cdot \hat{C} + w_{\text{lat}} \cdot \hat{L}$$

Providers in `Half-Open` state automatically receive a cooldown penalty (+10.0 score handicap) to prevent traffic thundering before health is fully established.

### Model Capability Validation & Cross-Family Fallbacks
* **Capability Validation**: Requests containing image URLs in messages, tool calls/function definitions, or `response_format: {"type": "json_object"}` are filtered against provider capabilities (`vision`, `tools`, `json`). Providers missing required capabilities are skipped.
* **Fallback Model Groups**: If all targets within a primary virtual model group are exhausted or failing, Phosphor cascades to the configured `default_fallbacks` (e.g., falling back to `gpt-4o-mini`).

### Exponential Backoff with Full Jitter
Transient retries calculate backoff with full jitter to avoid synchronous provider thundering:
$$t = \text{random}(0, \min(M, B \times 2^{\text{attempt}}))$$
where $B$ is the initial backoff (default 100ms) and $M$ is the maximum backoff ceiling (default 2000ms).

### High-Throughput Asynchronous SQLite Telemetry
HTTP requests write telemetry events into a non-blocking bounded Go channel (capacity 4096). A background worker batches pending events and executes a single multi-row SQLite transaction in WAL mode every 100ms or 50 events. 
All reading endpoints (`phosphor stats`, `phosphor logs`) call `db.Flush(ctx)` prior to querying to guarantee **Read-Your-Own-Writes** consistency without holding database locks on the request path. During graceful shutdown, the server invokes `db.Close()` which cleanly drains all buffered events.

### Zero-Allocation SSE Streaming Pipeline
The streaming engine uses a `sync.Pool` of reusable byte buffers for SSE chunk serialization, reducing memory overhead to ~1 alloc/op (595 ns/op).
Client context cancellation is detected immediately via `r.Context().Done()`, immediately closing the upstream HTTP request to stop upstream token generation and avoid unnecessary billing.

---

## Testing

Run unit tests, integration tests, and race-detector stress tests:

```bash
# Run all unit and integration tests
go test -v ./...

# Run race condition verification and stress tests
go test -v -race ./test -run TestGateway_ConcurrentStressAndFailover
```

---

## License
MIT License
