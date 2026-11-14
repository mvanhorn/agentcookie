"""Python reader for the agentcookie secrets bus.

    from agentcookie_secret import load

    env = load("my-pp-cli")
    token = env.get("MY_OAUTH_BEARER")

See docs/spec-agentcookie-secrets-bus-v1.md for the format and the
resolution order.
"""

from .load import (
    AgentcookieSecretError,
    InvalidCLINameError,
    LoadResult,
    Source,
    load,
    load_detailed,
)

__all__ = [
    "AgentcookieSecretError",
    "InvalidCLINameError",
    "LoadResult",
    "Source",
    "load",
    "load_detailed",
]
