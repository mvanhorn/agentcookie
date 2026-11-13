"""Tests for agentcookie_secret. Run with: python -m unittest discover -s clients/python/tests -t clients/python"""

import os
import stat
import tempfile
import textwrap
import unittest
from unittest import mock

from agentcookie_secret import (
    AgentcookieSecretError,
    InvalidCLINameError,
    Source,
    load,
    load_detailed,
)
from agentcookie_secret.load import _parse_env, _valid_cli_name


class _BusTestCase(unittest.TestCase):
    """Gives each test its own HOME and an empty PATH (no agentcookie)."""

    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.home = os.path.join(tmp.name, "home")
        self.bin_dir = os.path.join(tmp.name, "bin")
        os.makedirs(self.home)
        os.makedirs(self.bin_dir)
        patcher = mock.patch.dict(
            os.environ, {"HOME": self.home, "PATH": self.bin_dir}, clear=True
        )
        patcher.start()
        self.addCleanup(patcher.stop)

    def bus_path(self, cli, name="secrets.env"):
        return os.path.join(self.home, ".agentcookie", "secrets", cli, name)

    def write(self, path, content):
        os.makedirs(os.path.dirname(path), exist_ok=True)
        with open(path, "w", encoding="utf-8", newline="") as f:
            f.write(content)

    def fake_agentcookie(self, stdout="", stderr="", exit_code=0):
        """Install an agentcookie on PATH that records its argv and replies."""
        self.argv_log = os.path.join(self.bin_dir, "argv.log")
        out_file = os.path.join(self.bin_dir, "stdout")
        with open(out_file, "w", encoding="utf-8", newline="") as f:
            f.write(stdout)
        script = os.path.join(self.bin_dir, "agentcookie")
        with open(script, "w", encoding="utf-8") as f:
            f.write(
                textwrap.dedent(
                    f"""\
                    #!/bin/sh
                    printf '%s\\n' "$@" > '{self.argv_log}'
                    /bin/cat '{out_file}'
                    printf '%s' '{stderr}' >&2
                    exit {exit_code}
                    """
                )
            )
        os.chmod(script, stat.S_IRWXU)

    def recorded_argv(self):
        with open(self.argv_log, encoding="utf-8") as f:
            return f.read().splitlines()


class LoadTest(_BusTestCase):
    def test_invalid_cli_name_raises_before_touching_filesystem(self):
        for name in ["Bad-Name", "../etc", "a/b", "", "-x", "x-", "a--b", "a" * 65]:
            with self.subTest(name=name):
                with mock.patch("os.path.exists") as exists, mock.patch(
                    "builtins.open"
                ) as opened:
                    with self.assertRaises(InvalidCLINameError):
                        load(name)
                exists.assert_not_called()
                opened.assert_not_called()

    def test_invalid_cli_name_is_also_a_value_error(self):
        with self.assertRaises(ValueError):
            load("Bad")

    def test_plaintext_bus_only(self):
        self.write(self.bus_path("demo-cli"), "FOO=from-bus\nBAR=also-bus\n")
        env = load("demo-cli")
        self.assertEqual(env["FOO"], "from-bus")
        self.assertEqual(env["BAR"], "also-bus")

    def test_line_at_go_scanner_limit_raises(self):
        # Go rejects a raw line of 256 KiB or more, counting a '\r'.
        ok = "A=" + "x" * (256 * 1024 - 3)
        self.assertEqual(len(_parse_env(ok + "\n")["A"]), 256 * 1024 - 3)
        for text in (ok + "x\n", ok + "x", ok + "\r\n", "B=1\n" + ok + "xy\n"):
            with self.subTest(n=len(text)):
                with self.assertRaisesRegex(AgentcookieSecretError, "line (1|2): line too long"):
                    _parse_env(text)

    def test_only_go_whitespace_makes_a_blank_line(self):
        # str.strip() would skip these; Go's TrimSpace does not.
        for text in ("\x1c\n", "\x1f# c\n"):
            with self.subTest(text=text):
                with self.assertRaisesRegex(AgentcookieSecretError, "line 1: missing '='"):
                    _parse_env(text)
        self.assertEqual(_parse_env("\u3000\n\xa0# c\nA=1\n"), {"A": "1"})

    def test_leading_bom_is_ignored(self):
        self.write(self.bus_path("demo-cli"), "\ufeffFOO=from-bus\nBAR=2\n")
        env = load("demo-cli")
        self.assertEqual(env["FOO"], "from-bus")
        self.assertEqual(env["BAR"], "2")

    def test_bus_wins_over_env(self):
        os.environ["OAUTH_BEARER"] = "from-env-stale"
        self.write(self.bus_path("demo-cli"), "OAUTH_BEARER=from-bus-fresh\n")
        res = load_detailed("demo-cli")
        self.assertEqual(res.env["OAUTH_BEARER"], "from-bus-fresh")
        self.assertEqual(res.sources["OAUTH_BEARER"], Source.BUS_PLAIN)

    def test_empty_bus_value_still_wins_over_env(self):
        # Spec 11.3: KEY= is a real empty value, not an absent key.
        os.environ["TOKEN"] = "from-env"
        self.write(self.bus_path("demo-cli"), "TOKEN=\n")
        self.assertEqual(load("demo-cli")["TOKEN"], "")

    def test_fallback_between_bus_and_env(self):
        os.environ["FROM_ENV_ONLY"] = "env-val"
        fallback = os.path.join(self.home, "legacy-config.env")
        self.write(
            fallback, "FROM_FALLBACK=fallback-val\nFROM_ENV_ONLY=fallback-overrides-env\n"
        )
        self.write(
            self.bus_path("demo-cli"),
            "FROM_BUS=bus-val\nFROM_FALLBACK=bus-overrides-fallback\n",
        )
        res = load_detailed("demo-cli", fallback)
        self.assertEqual(
            (res.env["FROM_BUS"], res.sources["FROM_BUS"]), ("bus-val", Source.BUS_PLAIN)
        )
        self.assertEqual(
            (res.env["FROM_FALLBACK"], res.sources["FROM_FALLBACK"]),
            ("bus-overrides-fallback", Source.BUS_PLAIN),
        )
        self.assertEqual(
            (res.env["FROM_ENV_ONLY"], res.sources["FROM_ENV_ONLY"]),
            ("fallback-overrides-env", Source.FALLBACK),
        )

    def test_missing_or_malformed_fallback_is_ignored(self):
        os.environ["KEEP"] = "env-val"
        self.assertEqual(load("demo-cli", os.path.join(self.home, "nope.env"))["KEEP"], "env-val")
        bad = os.path.join(self.home, "bad.env")
        self.write(bad, "KEEP = spaced\n")
        self.assertEqual(load("demo-cli", bad)["KEEP"], "env-val")

    def test_no_bus_entry_falls_through_to_env(self):
        os.environ["STANDALONE_VAR"] = "shell-set"
        res = load_detailed("nonexistent-cli")
        self.assertEqual(res.env["STANDALONE_VAR"], "shell-set")
        self.assertEqual(res.sources["STANDALONE_VAR"], Source.ENV)

    def test_shell_internal_env_keys_are_skipped(self):
        os.environ.update({"PWD": "/x", "OLDPWD": "/y", "SHLVL": "2", "_": "/bin/sh"})
        env = load("demo-cli")
        for key in ("PWD", "OLDPWD", "SHLVL", "_"):
            self.assertNotIn(key, env)
        self.assertEqual(env["HOME"], self.home)

    def test_malformed_bus_raises_with_path_and_line(self):
        # Spec 11.4: never skip a bad line; name the file and the line.
        os.environ["OAUTH_BEARER"] = "from-env-stale"
        path = self.bus_path("demo-cli")
        self.write(path, "# comment\nOAUTH_BEARER=fresh\nNOT_A_PAIR\n")
        with self.assertRaises(AgentcookieSecretError) as cm:
            load("demo-cli")
        self.assertIn(path, str(cm.exception))
        self.assertIn("line 3", str(cm.exception))

    @unittest.skipIf(hasattr(os, "geteuid") and os.geteuid() == 0, "root ignores file modes")
    def test_unreadable_bus_raises(self):
        path = self.bus_path("demo-cli")
        self.write(path, "FOO=bar\n")
        os.chmod(path, 0)
        self.addCleanup(os.chmod, path, stat.S_IRUSR | stat.S_IWUSR)
        with self.assertRaises(AgentcookieSecretError) as cm:
            load("demo-cli")
        self.assertIn(path, str(cm.exception))
        self.assertIsInstance(cm.exception.__cause__, PermissionError)


class SealedTest(_BusTestCase):
    def test_sealed_unseals_through_agentcookie_and_wins(self):
        self.write(self.bus_path("demo-cli", "secrets.env.sealed"), "opaque")
        self.write(self.bus_path("demo-cli"), "SHARED=plain\nPLAIN_ONLY=p\n")
        os.environ["SHARED"] = "env"
        self.fake_agentcookie(stdout="SHARED=sealed\nSEALED_ONLY=s\n")

        res = load_detailed("demo-cli")

        self.assertEqual(self.recorded_argv(), ["secret", "env", "--no-aliases", "demo-cli"])
        self.assertEqual(
            (res.env["SHARED"], res.sources["SHARED"]), ("sealed", Source.BUS_SEALED)
        )
        self.assertEqual(
            (res.env["SEALED_ONLY"], res.sources["SEALED_ONLY"]), ("s", Source.BUS_SEALED)
        )
        self.assertEqual(
            (res.env["PLAIN_ONLY"], res.sources["PLAIN_ONLY"]), ("p", Source.BUS_PLAIN)
        )

    def test_sealed_output_quotes_are_stripped_once(self):
        # agentcookie quotes a value for the shell; one pair of quotes comes
        # off, so a stored value that itself has quotes, '=' or a '\r'
        # comes back as stored.
        self.write(self.bus_path("demo-cli", "secrets.env.sealed"), "opaque")
        self.fake_agentcookie(
            stdout="QUOTED='\"kept\"'\nSPACED='a b'\nAPOS=\"it's\"\nEQ=a=b\nCR='x\ry'\nEMPTY=\n"
        )
        env = load("demo-cli")
        self.assertEqual(env["QUOTED"], '"kept"')
        self.assertEqual(env["SPACED"], "a b")
        self.assertEqual(env["APOS"], "it's")
        self.assertEqual(env["EQ"], "a=b")
        self.assertEqual(env["CR"], "x\ry")
        self.assertEqual(env["EMPTY"], "")

    def test_sealed_without_agentcookie_on_path_raises(self):
        sealed = self.bus_path("demo-cli", "secrets.env.sealed")
        self.write(sealed, "opaque")
        self.write(self.bus_path("demo-cli"), "FOO=plain\n")
        with self.assertRaises(AgentcookieSecretError) as cm:
            load("demo-cli")
        self.assertIn(sealed, str(cm.exception))
        self.assertIn("agentcookie", str(cm.exception))
        self.assertIn("not on PATH", str(cm.exception))

    def test_sealed_unseal_failure_raises_with_stderr(self):
        sealed = self.bus_path("demo-cli", "secrets.env.sealed")
        self.write(sealed, "opaque")
        self.fake_agentcookie(stderr="read master key for sealed file: not found", exit_code=1)
        with self.assertRaises(AgentcookieSecretError) as cm:
            load("demo-cli")
        self.assertIn(sealed, str(cm.exception))
        self.assertIn("read master key", str(cm.exception))

    def test_sealed_unexpected_output_raises_without_echoing_it(self):
        self.write(self.bus_path("demo-cli", "secrets.env.sealed"), "opaque")
        self.fake_agentcookie(stdout="GOOD=1\nbad key=s3cr3t-value\n")
        with self.assertRaisesRegex(AgentcookieSecretError, "line 2 .* not KEY=VALUE") as cm:
            load("demo-cli")
        self.assertNotIn("s3cr3t-value", str(cm.exception))

    def test_old_agentcookie_without_no_aliases_gets_upgrade_hint(self):
        self.write(self.bus_path("demo-cli", "secrets.env.sealed"), "opaque")
        self.fake_agentcookie(stderr="Error: unknown flag: --no-aliases", exit_code=1)
        with self.assertRaisesRegex(AgentcookieSecretError, "upgrade agentcookie"):
            load("demo-cli")


class ParseEnvTest(unittest.TestCase):
    def test_quotes_stripped(self):
        got = _parse_env("A=\"hello world\"\nB='also quoted'\nC=plain\nD=\"mismatched'\n")
        self.assertEqual(
            got, {"A": "hello world", "B": "also quoted", "C": "plain", "D": "\"mismatched'"}
        )

    def test_backslash_continuation(self):
        self.assertEqual(_parse_env("LONG=part1\\\npart2\\\npart3\n"), {"LONG": "part1part2part3"})

    def test_unterminated_continuation_raises(self):
        for text in ("LONG=part1\\\n", "LONG=part1\\"):
            with self.subTest(text=text):
                with self.assertRaisesRegex(AgentcookieSecretError, "unterminated"):
                    _parse_env(text)
        # Same line numbers as the Go reader: the last line read, then the
        # line the continuation started on, and never the key.
        with self.assertRaisesRegex(
            AgentcookieSecretError, r"^line 4: unterminated backslash continuation \(started on line 3\)$"
        ):
            _parse_env("A=1\n# c\nLONG=part1\\\nmore\\\n")

    def test_crlf_line_endings(self):
        self.assertEqual(_parse_env("A=1\r\nB=2\r\n"), {"A": "1", "B": "2"})

    def test_comments_and_blank_lines_skipped(self):
        self.assertEqual(_parse_env("# c\n\n   \n  # indented\nA=1\n"), {"A": "1"})

    def test_rejects_whitespace_around_equals(self):
        for text in ("KEY = value\n", " KEY=value\n", "KEY\t=value\n"):
            with self.subTest(text=text):
                with self.assertRaisesRegex(AgentcookieSecretError, "line 1: whitespace"):
                    _parse_env(text)

    def test_rejects_invalid_key_names(self):
        for text in ("1KEY=v\n", "KEY-NAME=v\n", "KÉY=v\n", "=v\n"):
            with self.subTest(text=text):
                with self.assertRaisesRegex(AgentcookieSecretError, "line 1: invalid key name"):
                    _parse_env(text)

    def test_value_keeps_inner_equals_and_spaces(self):
        self.assertEqual(_parse_env("URL=a=b c\n_K1= lead\n"), {"URL": "a=b c", "_K1": " lead"})


class GoParityTest(_BusTestCase):
    """Behavior that must match the Go reader in pkg/agentcookiesecret."""

    def test_sealed_file_wins_over_malformed_plaintext(self):
        self.write(self.bus_path("demo-cli"), "NOT_A_PAIR\n")
        self.write(self.bus_path("demo-cli", "secrets.env.sealed"), "opaque")
        self.fake_agentcookie(stdout="A=1\n")
        res = load_detailed("demo-cli")
        self.assertEqual((res.env["A"], res.sources["A"]), ("1", Source.BUS_SEALED))

    @unittest.skipIf(hasattr(os, "geteuid") and os.geteuid() == 0, "root ignores file modes")
    def test_sealed_file_wins_over_unreadable_plaintext(self):
        path = self.bus_path("demo-cli")
        self.write(path, "FOO=bar\n")
        os.chmod(path, 0)
        self.addCleanup(os.chmod, path, stat.S_IRUSR | stat.S_IWUSR)
        self.write(self.bus_path("demo-cli", "secrets.env.sealed"), "opaque")
        self.fake_agentcookie(stdout="A=1\n")
        self.assertEqual(load("demo-cli")["A"], "1")

    def test_malformed_plaintext_without_sealed_file_raises(self):
        path = self.bus_path("demo-cli")
        self.write(path, "NOT_A_PAIR\n")
        with self.assertRaises(AgentcookieSecretError) as cm:
            load("demo-cli")
        self.assertIn(path, str(cm.exception))
        self.assertIn("line 1", str(cm.exception))

    def test_invalid_key_error_does_not_echo_the_line(self):
        self.write(self.bus_path("demo-cli"), "A=1\nsk-live+SECRET/abc=x\n")
        with self.assertRaises(AgentcookieSecretError) as cm:
            load("demo-cli")
        self.assertIn("line 2", str(cm.exception))
        self.assertNotIn("SECRET", str(cm.exception))
        self.assertNotIn("sk-live", str(cm.exception))

    def test_unterminated_continuation_does_not_echo_the_key(self):
        self.write(self.bus_path("demo-cli"), "A=1\nTOKEN=value\\\n")
        with self.assertRaises(AgentcookieSecretError) as cm:
            load("demo-cli")
        self.assertIn("line 2", str(cm.exception))
        self.assertNotIn("TOKEN", str(cm.exception))

    def test_leading_bom_is_ignored(self):
        path = self.bus_path("demo-cli")
        os.makedirs(os.path.dirname(path), exist_ok=True)
        with open(path, "wb") as f:
            f.write(b"\xef\xbb\xbfA=1\nB=2\n")
        env = load("demo-cli")
        self.assertEqual((env["A"], env["B"]), ("1", "2"))


class ValidCLINameTest(unittest.TestCase):
    def test_valid_cli_name(self):
        cases = {
            "foo": True,
            "foo-bar": True,
            "foo-pp-cli": True,
            "a" * 64: True,
            "Foo": False,
            "foo_bar": False,
            "foo.bar": False,
            "-foo": False,
            "foo-": False,
            "foo--bar": False,
            "": False,
            "a" * 65: False,
        }
        for name, want in cases.items():
            with self.subTest(name=name):
                self.assertEqual(_valid_cli_name(name), want)


if __name__ == "__main__":
    unittest.main()
