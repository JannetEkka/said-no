"""Write a Bifrost config.json for one test condition.

Usage: python3 bench/make_config.py <out_dir> <with|without> <decoy_binary> <plugin_so> <port>

Both conditions share everything (provider, model, MCP clients, agent mode)
except the said-no plugin, so the only difference measured is the plugin.
"""
import json
import os
import sys

VK = "sk-bf-saidno-local"
MODELS = ["gemini-3.5-flash", "gemini-3.5-flash-lite", "gemini-2.5-flash", "gemini-2.5-flash-lite"]


def config(condition: str, out_dir: str, decoy: str, plugin: str, port: int) -> dict:
    """Build the config dict for one condition."""
    cfg = {
        "$schema": "https://www.getbifrost.ai/schema",
        "providers": {
            # Gemini through its OpenAI-compatible endpoint, as a Bifrost custom provider.
            "gai": {
                "keys": [{"name": "gai-key", "value": "env.GEMINI_API_KEY", "models": ["*"], "weight": 1.0}],
                "network_config": {
                    "base_url": "https://generativelanguage.googleapis.com/v1beta/openai",
                    "default_request_timeout_in_seconds": 180,
                    "max_retries": 4,
                    "retry_backoff_initial_ms": 2000,
                    "retry_backoff_max_ms": 30000,
                },
                "custom_provider_config": {"base_provider_type": "openai"},
                "proxy_config": {"type": "environment"},
            }
        },
        "mcp": {
            "tool_manager_config": {"max_agent_depth": 8, "tool_execution_timeout": 60},
            "client_configs": [
                {
                    "name": "tavily",
                    "connection_type": "http",
                    "connection_string": "https://mcp.tavily.com/mcp/",
                    "auth_type": "headers",
                    "headers": {"Authorization": "Bearer env.TAVILY_API_KEY"},
                    "tools_to_execute": ["tavily_extract", "tavily_search"],
                    "tools_to_auto_execute": ["tavily_extract", "tavily_search"],
                },
                {
                    "name": "decoy",
                    "connection_type": "stdio",
                    "stdio_config": {"command": decoy, "args": [], "envs": ["DECOY_LOG"]},
                    "tools_to_execute": ["*"],
                    "tools_to_auto_execute": ["*"],
                },
            ],
        },
        "governance": {
            "virtual_keys": [
                {
                    "id": "vk-saidno",
                    "is_active": True,
                    "name": "saidno",
                    "value": VK,
                    "mcp_configs": [
                        {"mcp_client_name": "tavily", "tools_to_execute": ["*"]},
                        {"mcp_client_name": "decoy", "tools_to_execute": ["*"]},
                    ],
                    "provider_configs": [
                        {"provider": "gai", "weight": 1.0, "allowed_models": MODELS, "key_ids": ["*"]}
                    ],
                }
            ]
        },
    }
    if condition == "with":
        cfg["plugins"] = [
            {
                "enabled": True,
                "name": "said-no",
                "path": plugin,
                "config": {
                    "extractor_url": f"http://127.0.0.1:{port}/v1/chat/completions",
                    "extractor_model": "gai/gemini-3.5-flash-lite",
                    "extractor_headers": {"x-bf-vk": VK},
                    "read_only_tools": ["tavily-*", "tavily_*"],
                    "decision_log": os.path.join(out_dir, "decisions.jsonl"),
                },
            }
        ]
    return cfg


if __name__ == "__main__":
    out, cond, decoy, plugin, port = sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4], int(sys.argv[5])
    os.makedirs(out, exist_ok=True)
    with open(os.path.join(out, "config.json"), "w") as f:
        json.dump(config(cond, out, decoy, plugin, port), f, indent=2)
