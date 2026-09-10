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

* **OpenAI-Compatible Inbound API**: Transparent drop-in replacement for OpenAI SDKs, LangChain, LlamaIndex, LiteLLM, or `curl`. Serves `POST /v1/chat/completions`, `GET /v1/models`, and `GET /health`.
* **Zero-Allocation SSE Streaming Pipeline**: True low-latency chunk streaming with `http.Flusher`, capturing Time-To-First-Token (TTFT) on initial chunk arrival.
* **Smart Dynamic Routing**:
  * `priority`: Sequential priority list with fallback.
  * `least-cost`: Pre-calculates estimated prompt cost and routes to the cheapest eligible upstream provider.
  * `lowest-latency`: Routes to the fastest upstream using an internal rolling Exponential Moving Average (EMA) of Time-To-First-Token (TTFT).
* **Resilient Failover Engine & Circuit Breakers**:
  * Automatically cascades to fallback providers on HTTP 429 (Rate Limit), 5xx (Server Error), or network timeout.
  * Consecutive transient errors trip the provider circuit breaker (`Closed` $\rightarrow$ `Open` $\rightarrow$ `Half-Open`) to prevent latency stampedes.
* **Pure-Go SQLite Telemetry**:
  * Powered by `modernc.org/sqlite` (100% CGo-free).
  * Records request metadata, token consumption, estimated dollar cost, TTFT, and failover traces.
* **Terminal-First Dashboard**:
  * `phosphor stats`: Styled terminal dashboard displaying spend cards, token usage, and provider health using `charmbracelet/lipgloss`.
  * `phosphor logs`: Real-time request log inspection and visual failover cascade traces (`OpenAI ➔ Groq`).

---

## Supported Upstream Providers

| Provider | Type | Supported APIs | Protocol |
| :--- | :--- | :--- | :--- |
| **OpenAI** | `openai` | `gpt-4o`, `gpt-4o-mini`, `o1`, `o3-mini` | REST / SSE |
| **Groq** | `groq` | `llama-3.3-70b-versatile`, `llama-3.1-8b-instant` | OpenAI-compatible REST / SSE |
| **Anthropic** | `anthropic` | `claude-3-5-sonnet-20241022`, `claude-3-5-haiku-20241022` | Claude Messages API translation |
| **Gemini** | `gemini` | `gemini-2.0-flash`, `gemini-1.5-pro` | Google OpenAI-compat REST / SSE |
| **Ollama** | `ollama` | `llama3.2`, `mistral`, `deepseek-r1` | Local daemon (`localhost:11434`) |

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

### Circuit Breaker State Machine
Every upstream provider has an isolated circuit breaker managing transient error conditions (HTTP 429, HTTP 5xx, context deadlines, network timeouts):

```
       +------------------+
       |   CLOSED (OK)    | <-------------+
       +------------------+               |
                 |                        |
         consecutive errors >= threshold  | probe succeeds
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

### Routing Strategies
* **`priority`**: Evaluates candidates in user-configured order. If provider 1 fails or is tripped, cascades immediately to provider 2.
* **`least-cost`**: Evaluates prompt tokens with a heuristic (~4 chars/token + framing tokens) and calculates:
  $$\text{Estimated Cost} = \frac{\text{PromptTokens}}{1,000,000} \times \text{Cost}_{\text{prompt}} + \frac{\text{EstCompletionTokens}}{1,000,000} \times \text{Cost}_{\text{completion}}$$
  Candidates are dynamically sorted in ascending order of cost.
* **`lowest-latency`**: Compares rolling Exponential Moving Average (EMA) of Time-To-First-Token (TTFT):
  $$\text{EMA}_{\text{new}} = (\alpha \times \text{TTFT}_{\text{observed}}) + ((1 - \alpha) \times \text{EMA}_{\text{prev}})$$
  Routes each request to the provider demonstrating the lowest TTFT.

---

## Testing

Run unit tests and end-to-end integration tests:

```bash
# Run all unit and integration tests
go test -v ./...
```

---

## License
MIT License
