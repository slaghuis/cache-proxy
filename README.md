 # LiteLLM Middleware with Semantic Cache & Smart Routing
A Go-based proxy that sits between agents and LiteLLM, adding a Qdrant-backed semantic cache, confidence-gated local→cloud escalation, and cost tracking. OpenAI-compatible so any agent (opencode, Cursor, Claude Code via LiteLLM) points at it transparently.

## Architecture
```
┌──────────────────────────────────────────────┐
│  Agent (opencode / Cursor / Claude Code)     │
│  OPENAI_BASE_URL=http://localhost:8080/v1    │
└──────────────────────────────────────────────┘
                      │
                      ▼
┌──────────────────────────────────────────────┐
│  Semantic Cache Middleware  (Go, port 8080)  │
│  1. Hash exact match?   → return $0          │
│  2. Semantic match?     → return $0          │
│  3. Route: local or cloud?                   │
│  4. Call LiteLLM                             │
│  5. Store response in Qdrant                 │
│  6. Record cost in SQLite                    │
└──────────────────────────────────────────────┘
      │                       │
      ▼                       ▼
┌──────────────┐      ┌─────────────────────┐
│ LiteLLM      │      │ Qdrant              │
│ :4000        │      │ semantic_cache      │
│ (routes to   │      │ collection          │
│  Ollama,     │      └─────────────────────┘
│  Anthropic,  │
│  OpenAI...)  │
└──────────────┘
```
Why two layers (middleware + LiteLLM)?
 - LiteLLM handles provider SDK normalization, retries, fallbacks, rate limits — things done well in Python.
 - Our middleware owns the decisions: cache lookups, routing policy, cost tracking. All in Go, zero external dependencies.

 ## Design Decisions
 | Decision | Choice | Why |
 | -------- | ------ | --- | 
 | Protocol | OpenAI-compatible /v1/chat/completions| Universal; agents already speak it |
 | Cache storage | Qdrant collection semantic_cache (1024-dim via bge-m3) | Separate from code; different embedder optimal for prompts |
 | Exact-match cache | SQLite with SHA256 of normalized request | Faster than vector search for identical calls |
 | Semantic threshold | 0.95 by default, configurable per model | Conservative; false positives are expensive  | 
 | Cache bypass | Honor temperature>0.3, tools present, stream=true | Semantic cache is only safe for deterministic calls | 
 | Routing | Rule-based: by prompt size, model hint, task tag | Simple, debuggable, no "AI picking AI" surprises |
 | Cost tracking | SQLite with per-call rows | Grafana-friendly, auditable |
 | Streaming | Pass through, cache the final assembled message | Agents expect streaming for UX |
 | Config | YAML with hot reload via SIGHUP | Tune routing without restart |

 ## Prerequisites: Install LiteLLM
```
pipx install 'litellm[proxy]'

# Minimal LiteLLM config - ~/.litellm/config.yaml
```
Edit file `~/.litellm/config.yaml`
```
# ~/.litellm/config.yaml
model_list:
  - model_name: claude-sonnet
    litellm_params:
      model: anthropic/claude-sonnet-4-5-20250929
      api_key: os.environ/ANTHROPIC_API_KEY

  - model_name: gpt-5
    litellm_params:
      model: openai/gpt-5
      api_key: os.environ/OPENAI_API_KEY

  - model_name: qwen-coder
    litellm_params:
      model: ollama/qwen2.5-coder:14b
      api_base: http://localhost:11434

  - model_name: qwen-coder-small
    litellm_params:
      model: ollama/qwen2.5-coder:7b
      api_base: http://localhost:11434

litellm_settings:
  drop_params: true
  set_verbose: false

general_settings:
  master_key: sk-local-dev
```
Start LiteLLM:
```
litellm --config ~/.litellm/config.yaml --port 4000
```
Verify
```
curl -s http://localhost:4000/v1/models -H "Authorization: Bearer sk-local-dev" | jq .
```
 ## Create the Semantic Cache Collection
```
curl -X PUT http://localhost:6333/collections/semantic_cache \
  -H 'Content-Type: application/json' \
  -d '{
    "vectors": { "size": 1024, "distance": "Cosine" }
  }'

# Pull the embedding model for prompts
ollama pull bge-m3
```
We use `bge-m3` (1024-dim) for prompts because it handles instructions and multilingual content better than `nomic-embed-text`. Deliberately different from the code embedder — don't mix dimensions in one collection.

 ## Build & Run
```
cd cache-proxy
go build -o ~/.local/bin/cache-proxy ./cmd/cache-proxy

~/.local/bin/cache-proxy -config ./config.yaml
```

Point agents at it:
```
export OPENAI_BASE_URL=http://localhost:8080/v1
export OPENAI_API_KEY=sk-local-dev
```
Test manually
```
# First call — miss
curl -s http://localhost:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "claude-sonnet",
    "messages": [{"role":"user","content":"Explain Go channels in one paragraph."}],
    "temperature": 0.1
  }' | jq '.x_cache, .choices[0].message.content'

# Second call — exact hit (identical body)
# (same curl again) → x_cache: "exact"

# Third call — semantic hit (slightly different wording)
curl -s http://localhost:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "claude-sonnet",
    "messages": [{"role":"user","content":"Could you give me a one-paragraph explanation of channels in Go?"}],
    "temperature": 0.1
  }' | jq '.x_cache, .x_cache_score'
```
Check stats
```
curl -s http://localhost:8080/admin/stats?hours=24 | jq .
```
 ## Wiring to Agents

 ### opencode
Edit file `~/.config/opencode/opencode.json`
```
{
  "providers": {
    "cache-proxy-strict": {
      "type": "openai",
      "baseURL": "http://localhost:8080/v1",
      "apiKey": "sk-local-dev",
      "defaultHeaders": {
        "x-task-tag": "debug",
        "x-escalation": "auto"
      }
    },
    "cache-proxy-cheap": {
      "type": "openai",
      "baseURL": "http://localhost:8080/v1",
      "apiKey": "sk-local-dev",
      "defaultHeaders": {
        "x-task-tag": "boilerplate",
        "x-escalation": "local-only"
      }
    }
  }
}
```
 ### Cursor
Settings -> Models 0> Add OpenAI base URL `http://localhost:8080/v1`.

 ### Pass task tags from scripts
```
curl -s http://localhost:8080/v1/chat/completions \
  -H 'x-task-tag: refactor' \
  -H 'Content-Type: application/json' \
  -d '{ ... }'
```

## Improvement - Confidence-Gated Local-First Routing
Add intelligent escalation to the cache-proxy. Try a local model first (free), inspect the output for signs it got the task right, and only pay for cloud models when local can't deliver.
 ### The Core Idea
Current cache-proxy routing is static: a rule picks a model based on prompt size or task tag. We're adding a dynamic layer on top:
```
Request arrives
  │
  ├─ Static router picks: "qwen-coder" (local)   ← existing behaviour
  │
  ├─ Call local model
  │
  ├─ Score the response:
  │   • Tool-call sanity
  │   • Response length vs prompt complexity  
  │   • Explicit "I don't know" markers
  │   • Compilation / syntax check (for code tasks)
  │   • Self-reported confidence (optional)
  │
  ├─ Score ≥ threshold?  →  return local response, log savings
  │
  └─ Score < threshold   →  escalate to cloud (Claude), 
                             tag response as "escalated"
```
Key design principle: cheap signals only. If scoring costs more than a Claude call would have, we've lost. Everything happens locally, in milliseconds.
 ### Design Decisions
 | Decision | Choice | Rationale |
 | -------- | ------ | --------- |
 | Where it lives | New escalator package inside cache-proxy | Keeps routing + escalation co-located |
 | What triggers escalation | Composable signal functions returning (score, reason) | Easy to tune per task type |
 | Signals included | Length ratio, refusal markers, Go compile check, structural coherence | Cheap, high-signal for Go dev |
 | Opt-in per request | Honor x-escalation header: auto (default), local-only, cloud-only, escalate-always | Agents can force when needed |
 | Per-task-tag policies | YAML config maps tags to escalation profiles | "refactor" is strict; "explain" is loose |
 | Cache interaction | Cache stores both local-good and cloud responses separately | Never serve local to a request that would have escalated |
 | Observability | Record escalated reason in ledger | Can tune thresholds from data |
 | Failure modes | If local fails/times out, go to cloud; never fail-closed | User experience > cost savings |

### Scoring Signals (Go-Dev-Specific)
Each signal returns a score 0.0–1.0 where 1.0 means "local looks great". We combine them with weighted average.
#### Length ratio
A one-sentence reply to a 2000-token prompt is suspicious. A 10-line reply to "fix the typo" is fine. Compute output_tokens / expected_output_tokens. The "expected" is tuned per task type.
#### Refusal markers
Local models often hedge: "I'm not sure, but...", "As an AI...", "I cannot...". Simple regex catches these. If found, score = 0.
#### Code block syntax
If the prompt looks code-related and the response contains a Go code block, try parsing it with go/parser. Compile errors = low score. This is the strongest signal for coding work.
#### Structural coherence
Does the response have abrupt truncation? Unbalanced braces? Markdown that opens with ### but never closes a code fence? Cheap to detect.
#### Prompt-answer alignment (optional)
Embed the prompt and the response, compute cosine similarity, ensure it's above a floor. Catches "my cat is cute" responses to Go questions. Uses Ollama embeddings you already have.
#### Self-confidence (optional, more expensive)
Second prompt to the local model: "Rate your confidence 0-10 in the above answer." Adds latency; use only for high-stakes task tags.


## Testing End-to-End
```
# Build and restart cache-proxy
cd ~/code/ai-factory/cache-proxy
go build -o ~/.local/bin/cache-proxy ./cmd/cache-proxy
# restart your running instance

# 1. A simple task — should pass locally
curl -s http://localhost:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -H 'x-task-tag: boilerplate' \
  -d '{
    "model":"auto",
    "messages":[{"role":"user","content":"Write a Go function that returns the sum of two ints."}],
    "temperature":0.1
  }' -D -

# Expected headers:
#   x-model-used: qwen-coder-small
#   x-escalated: false
#   x-score: 0.9+

# 2. A complex task with code — may escalate
curl -s http://localhost:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -H 'x-task-tag: refactor' \
  -d '{
    "model":"auto",
    "messages":[{"role":"user","content":"Refactor this to use generics and add a cache: func SumInts(xs []int) int { s:=0; for _,x:=range xs { s+=x }; return s }"}],
    "temperature":0.1
  }' -D -

# Expected (if local output is weak):
#   x-model-used: claude-sonnet
#   x-escalated: true
#   x-escalation-reason: low confidence: go_parse(Go block 1 did not parse: ...)

# 3. Force local only
curl -s http://localhost:8080/v1/chat/completions \
  -H 'x-escalation: local-only' \
  ...
# 4. See the stats
curl -s http://localhost:8080/admin/stats?hours=1 | jq
# {
#   "total_calls": 10,
#   "cache_hits": 2,
#   "escalations": 3,
#   "local_successes": 5,
#   "local_success_rate": 0.625,
#   "cost_usd": 0.08,
#   "saved_usd_estimate": 0.25
# }
```

 ## Tuning Playbook
Watch the stats for a week and adjust:
 | Observation | Likely Cause | Fix |
 | ----------- | ------------ | --- |
 | `local_success_rate < 0.3` | Threshold too high, or local model too weak for your tasks | Lower profile threshold by 0.05–0.1, or try `qwen-coder` instead of `qwen-coder-small` |
 | `local_success_rate > 0.9` | Threshold too low; you're accepting bad local outputs | Raise threshold by 0.05–0.1 |
 | Lots of escalations with `go_parse` reasons | Local model struggles with Go specifically | Switch local to `deepseek-coder-v2:7b` or similar |
 | Lots of escalations with length reasons | `expected_output_tokens` is miscalibrated | Adjust per profile based on real response sizes |
 | Latency complaints | Local model is slow and often fails | Shorten `local_timeout_seconds` or route certain tags straight to cloud |
 | Users report wrong-model answers | Scoring too permissive on hard tasks | Add `enable_similarity_check: true` to that tag's profile |

## What You Gain
Realistic baseline for Go dev work:
 - **Boilerplate / explain** tags: 70–90% local success → 70–90% $0 responses.
 - **Refactor / debug** tags: 20–40% local success → 20–40% cost savings on tasks that would otherwise be 100% cloud.
 - **Overall**: another 20–40% cloud spend reduction on top of what the cache already saves.
Combined with the cache and MCP retrieval, you're now at a realistic 70–85% cloud cost reduction vs naive agent usage, with no meaningful quality regression because escalation catches bad local answers automatically.

 ## Operational Notes
 - **Threshold tuning**: start at 0.95. If you're seeing stale answers, raise to 0.97. If hit rate is low on obviously similar questions, drop to 0.92 and watch for regressions.
 - **Scoped cache per model**: a Claude response cached under "claude-sonnet" is NOT served for "gpt-5" queries. This is intentional — different models produce different styles.
 - **Streaming hits**: when a cached response is served to a streaming client, we fake a single SSE chunk. Agents treat this as instant streaming. UX stays identical.
 - **Tool calls bypass cache**: tool-use flows are stateful and the agent will almost always want a fresh call. Cacheable() returns false if `tools` is present.
 - **Hot config reload**: `kill -HUP $(pgrep cache-proxy)` reloads routing rules and pricing without restarting.
 - **Observability**: pipe stats to Grafana via SQLite exporter, or just curl `/admin/stats` periodically from a dashboard.
