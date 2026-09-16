package proxy

import (
	"net/http"
)

const openAPISpec = `{
  "openapi": "3.1.0",
  "info": {
    "title": "Phosphor LLM Gateway",
    "description": "High-performance reverse proxy for LLMs with multi-provider routing, failover, caching, and guardrails.",
    "version": "0.1.0"
  },
  "paths": {
    "/v1/chat/completions": {
      "post": {
        "summary": "Create chat completion",
        "description": "Generate model responses with optional streaming SSE, routing failover, and prompt caching.",
        "operationId": "createChatCompletion",
        "requestBody": {
          "required": true,
          "content": {
            "application/json": {
              "schema": {
                "type": "object",
                "required": ["model", "messages"],
                "properties": {
                  "model": {"type": "string"},
                  "messages": {
                    "type": "array",
                    "items": {
                      "type": "object",
                      "required": ["role", "content"],
                      "properties": {
                        "role": {"type": "string", "enum": ["system", "user", "assistant"]},
                        "content": {"type": "string"}
                      }
                    }
                  },
                  "temperature": {"type": "number"},
                  "max_tokens": {"type": "integer"},
                  "stream": {"type": "boolean"}
                }
              }
            }
          }
        },
        "responses": {
          "200": {"description": "Chat completion response or SSE stream"},
          "400": {"description": "Bad request or guardrail rejection"},
          "401": {"description": "Unauthorized API key"},
          "429": {"description": "Rate limit or budget exceeded"},
          "502": {"description": "All upstream providers failed"}
        }
      }
    },
    "/v1/models": {
      "get": {
        "summary": "List models",
        "description": "Returns list of configured model targets and aliases.",
        "responses": {
          "200": {"description": "List of available models"}
        }
      }
    },
    "/health": {
      "get": {
        "summary": "Health probe",
        "description": "Returns gateway process health status.",
        "responses": {
          "200": {"description": "Service is healthy"}
        }
      }
    },
    "/ready": {
      "get": {
        "summary": "Readiness probe",
        "description": "Returns gateway readiness status including database and provider circuit states.",
        "responses": {
          "200": {"description": "Service is ready"}
        }
      }
    },
    "/metrics": {
      "get": {
        "summary": "Prometheus metrics",
        "description": "Prometheus formatted metrics exporter.",
        "responses": {
          "200": {"description": "Prometheus metrics output"}
        }
      }
    },
    "/openapi.json": {
      "get": {
        "summary": "OpenAPI Specification",
        "description": "Returns OpenAPI 3.1.0 specification for Phosphor gateway.",
        "responses": {
          "200": {"description": "OpenAPI schema document"}
        }
      }
    },
    "/v1/admin/budgets": {
      "get": {
        "summary": "Client budgets",
        "description": "Returns current spend and limits for all authenticated clients.",
        "responses": {
          "200": {"description": "Client budget status list"}
        }
      }
    },
    "/v1/admin/cache/stats": {
      "get": {
        "summary": "Cache statistics",
        "description": "Returns prompt response cache hit/miss metrics.",
        "responses": {
          "200": {"description": "Cache statistics"}
        }
      }
    },
    "/v1/admin/cache/clear": {
      "post": {
        "summary": "Clear cache",
        "description": "Purges memory and database response caches.",
        "responses": {
          "200": {"description": "Cache purged successfully"}
        }
      }
    },
    "/v1/admin/db/vacuum": {
      "post": {
        "summary": "Database vacuum",
        "description": "Compacts SQLite database and optimizes indexes.",
        "responses": {
          "200": {"description": "Database compacted successfully"}
        }
      }
    }
  },
  "components": {
    "securitySchemes": {
      "BearerAuth": {
        "type": "http",
        "scheme": "bearer"
      },
      "ApiKeyAuth": {
        "type": "apiKey",
        "in": "header",
        "name": "x-api-key"
      }
    }
  },
  "security": [
    {"BearerAuth": []},
    {"ApiKeyAuth": []}
  ]
}`

func (s *Server) handleOpenAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "Method not allowed. Only GET is supported.", "invalid_request_error", "method_not_allowed")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(openAPISpec))
}
