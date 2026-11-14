"""Reader for the agentcookie secrets bus.

Follows the Go reference reader in pkg/agentcookiesecret and section 11 of
docs/spec-agentcookie-secrets-bus-v1.md. Highest priority first:

1. ~/.agentcookie/secrets/<cli>/secrets.env.sealed, unsealed by running
   ``agentcookie secret env --no-aliases <cli>``. The master key lives in
   the macOS Keychain under agentcookie's own access rules, so this module
   never reads it.
2. ~/.agentcookie/secrets/<cli>/secrets.env
3. An optional caller-supplied fallback file in the same KEY=VALUE format.
4. Process environment variables.

A key found in a higher-priority source wins. The module never writes.
"""

from __future__ import annotations

import enum
import os
import shutil
import subprocess
from dataclasses import dataclass, field
from typing import Dict, List, Optional

__all__ = [
    "AgentcookieSecretError",
    "InvalidCLINameError",
    "LoadResult",
    "Source",
    "load",
    "load_detailed",
]

# Shell-internal variables the Go reader leaves out of the env layer.
_SKIPPED_ENV_KEYS = frozenset({"PWD", "OLDPWD", "SHLVL", "_"})

_ASCII_LETTERS = frozenset("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ")
_ASCII_DIGITS = frozenset("0123456789")
_CLI_NAME_CHARS = frozenset("abcdefghijklmnopqrstuvwxyz0123456789-")

# The characters Go's strings.TrimSpace trims (unicode.IsSpace). Python's
# str.strip() also trims \x1c-\x1f, which Go does not.
_GO_SPACE = (
    " \t\n\v\f\r\x85\xa0\u1680\u2000\u2001\u2002\u2003\u2004\u2005\u2006"
    "\u2007\u2008\u2009\u200a\u2028\u2029\u202f\u205f\u3000"
)

# The Go reader scans with a 256 KiB buffer, so a raw line (counting a
# trailing '\r', not the '\n') of this many bytes or more is an error.
_MAX_LINE_BYTES = 256 * 1024


class AgentcookieSecretError(Exception):
    """Raised when the bus cannot be read for a CLI."""


class InvalidCLINameError(AgentcookieSecretError, ValueError):
    """Raised when a CLI name breaks the v1 naming rules (spec section 2.3)."""


class Source(enum.Enum):
    """Where a key's value came from."""

    BUS_SEALED = "bus_sealed"
    BUS_PLAIN = "bus_plain"
    FALLBACK = "fallback"
    ENV = "env"


@dataclass
class LoadResult:
    """The merged map plus the source of each key (same keys as ``env``)."""

    env: Dict[str, str] = field(default_factory=dict)
    sources: Dict[str, Source] = field(default_factory=dict)

    def _merge(self, values: Dict[str, str], source: Source) -> None:
        for key, value in values.items():
            self.env[key] = value
            self.sources[key] = source


def load(cli_name: str, fallback_path: Optional[str] = None) -> Dict[str, str]:
    """Return the secrets for ``cli_name`` as a flat dict.

    ``fallback_path`` names an existing KEY=VALUE file (for example, the
    CLI's own config from before it adopted the bus). It is consulted
    after the bus and before process env; a missing or unparsable
    fallback file is ignored.

    A missing bus entry is not an error: the result then holds the
    fallback file and process env. Raises ``InvalidCLINameError`` before
    touching the filesystem for a bad name, and ``AgentcookieSecretError``
    when secrets.env does not parse or cannot be read, or when a sealed
    file is present but cannot be unsealed. Errors name the file, and a
    syntax error also names the line.
    """
    return load_detailed(cli_name, fallback_path).env


def load_detailed(cli_name: str, fallback_path: Optional[str] = None) -> LoadResult:
    """Like ``load``, but also reports where each key came from."""
    if not _valid_cli_name(cli_name):
        raise InvalidCLINameError(
            f"agentcookie_secret: cli name violates v1 naming rules: {cli_name!r}"
        )

    result = LoadResult()

    # Lowest priority first; each later layer overwrites the keys it has.
    result._merge(
        {k: v for k, v in os.environ.items() if k not in _SKIPPED_ENV_KEYS},
        Source.ENV,
    )

    if fallback_path:
        try:
            result._merge(_read_env_file(fallback_path), Source.FALLBACK)
        except (OSError, AgentcookieSecretError):
            pass

    plain_path = os.path.join(
        os.path.expanduser("~"), ".agentcookie", "secrets", cli_name, "secrets.env"
    )
    sealed_path = plain_path + ".sealed"
    # A sealed file is authoritative (spec section 5.2): when it exists, a
    # bad plaintext sibling is ignored rather than raised, as in the Go
    # reader.
    sealed_present = os.path.exists(sealed_path)

    plain: Dict[str, str] = {}
    plain_error: Optional[AgentcookieSecretError] = None
    try:
        plain = _read_env_file(plain_path)
    except FileNotFoundError:
        pass
    except OSError as exc:
        plain_error = AgentcookieSecretError(f"read {plain_path}: {exc}")
        plain_error.__cause__ = exc  # as `raise ... from exc` would set
    except AgentcookieSecretError as exc:
        plain_error = exc
    if not sealed_present:
        # A writer seals by creating the sealed file and then removing the
        # plaintext one. Check again so a seal that lands between the first
        # check and the plaintext read is still found.
        sealed_present = os.path.exists(sealed_path)
    if plain_error is not None and not sealed_present:
        raise plain_error
    result._merge(plain, Source.BUS_PLAIN)

    if sealed_present:
        result._merge(_unseal(cli_name, sealed_path), Source.BUS_SEALED)

    return result


def _unseal(cli_name: str, sealed_path: str) -> Dict[str, str]:
    binary = shutil.which("agentcookie")
    if binary is None:
        raise AgentcookieSecretError(
            f"{sealed_path} is sealed and reading it needs the agentcookie "
            "binary, which is not on PATH"
        )
    try:
        proc = subprocess.run(
            [binary, "secret", "env", "--no-aliases", cli_name],
            capture_output=True,
            check=False,
        )
    except OSError as exc:
        raise AgentcookieSecretError(f"unseal {sealed_path}: run {binary}: {exc}") from exc
    if proc.returncode != 0:
        stderr = proc.stderr.decode("utf-8", errors="replace").strip()
        detail = stderr or f"exit status {proc.returncode}"
        if "unknown flag: --no-aliases" in stderr:
            detail += " (upgrade agentcookie to a release with `secret env --no-aliases`)"
        raise AgentcookieSecretError(f"unseal {sealed_path}: {detail}")

    # `secret env` prints each parsed value verbatim as KEY=VALUE, one line
    # per key, so split on the first '=' and leave quotes alone: they were
    # already stripped when agentcookie parsed the file. Output is decoded
    # by hand because text mode would turn a '\r' inside a value into a
    # line break.
    out: Dict[str, str] = {}
    lines = proc.stdout.decode("utf-8", errors="surrogateescape").split("\n")
    if lines[-1] == "":
        lines.pop()
    for line_num, line in enumerate(lines, start=1):
        key, sep, value = line.partition("=")
        if not sep or not _valid_key_name(key):
            # Never echo the line: it may hold a secret value.
            raise AgentcookieSecretError(
                f"unseal {sealed_path}: line {line_num} of `{binary} secret env` "
                "output is not KEY=VALUE"
            )
        out[key] = value
    return out


def _read_env_file(path: str) -> Dict[str, str]:
    with open(path, "rb") as f:
        data = f.read()
    try:
        # surrogateescape keeps every byte, so line lengths match Go's.
        return _parse_env(data.decode("utf-8", errors="surrogateescape"))
    except AgentcookieSecretError as exc:
        raise AgentcookieSecretError(f"read {path}: {exc}") from None


def _split_lines(text: str) -> List[str]:
    # Same line splitting as Go's bufio.ScanLines, before it drops a
    # trailing '\r': split on '\n', with no empty final line after a
    # trailing '\n'.
    lines = text.split("\n")
    if lines and lines[-1] == "":
        lines.pop()
    return lines


def _parse_env(text: str) -> Dict[str, str]:
    """Parse the strict v1 dotenv subset, as the Go reader does."""
    out: Dict[str, str] = {}
    pending: List[str] = []
    pending_key = ""
    pending_line = 0
    in_continuation = False

    line_num = 0
    for line_num, raw in enumerate(_split_lines(text), start=1):
        if len(raw.encode("utf-8", errors="surrogateescape")) >= _MAX_LINE_BYTES:
            raise AgentcookieSecretError(f"line {line_num}: line too long (limit 256 KiB)")
        line = raw[:-1] if raw.endswith("\r") else raw
        if line_num == 1 and line.startswith("\ufeff"):
            # Spec section 3.1: a leading UTF-8 BOM is permitted and ignored.
            line = line[1:]
        if in_continuation:
            if line.endswith("\\"):
                pending.append(line[:-1])
                continue
            pending.append(line)
            out[pending_key] = _strip_quotes("".join(pending))
            pending = []
            pending_key = ""
            in_continuation = False
            continue

        trimmed = line.strip(_GO_SPACE)
        if trimmed == "" or trimmed.startswith("#"):
            continue
        key, sep, value = line.partition("=")
        if not sep:
            raise AgentcookieSecretError(
                f"line {line_num}: missing '=' (expected KEY=VALUE)"
            )
        if key != key.strip(" \t"):
            raise AgentcookieSecretError(
                f"line {line_num}: whitespace around '=' is not allowed"
            )
        if not _valid_key_name(key):
            # Never echo the key: a bare secret pasted on its own line
            # lands here.
            raise AgentcookieSecretError(f"line {line_num}: invalid key name")

        if value.endswith("\\"):
            pending.append(value[:-1])
            pending_key = key
            pending_line = line_num
            in_continuation = True
            continue
        out[key] = _strip_quotes(value)

    if in_continuation:
        # Same text and line numbers as the Go reader: the last line read,
        # then the line the continuation started on. Never the key.
        raise AgentcookieSecretError(
            f"line {line_num}: unterminated backslash continuation "
            f"(started on line {pending_line})"
        )
    return out


def _strip_quotes(value: str) -> str:
    if len(value) >= 2 and value[0] == value[-1] and value[0] in "\"'":
        return value[1:-1]
    return value


def _valid_cli_name(name: object) -> bool:
    if not isinstance(name, str) or not name or len(name) > 64:
        return False
    if name[0] == "-" or name[-1] == "-" or "--" in name:
        return False
    return all(c in _CLI_NAME_CHARS for c in name)


def _valid_key_name(key: str) -> bool:
    if not key:
        return False
    if key[0] not in _ASCII_LETTERS and key[0] != "_":
        return False
    return all(c in _ASCII_LETTERS or c in _ASCII_DIGITS or c == "_" for c in key)
