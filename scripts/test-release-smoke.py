"""Contract tests for release smoke evidence validation."""
import importlib.util
from pathlib import Path
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("release_smoke", Path(__file__).with_name("release-smoke.py"))
smoke = importlib.util.module_from_spec(spec)
spec.loader.exec_module(smoke)


class SetupValidationTests(unittest.TestCase):
    def test_rejects_config_that_would_read_another_project(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            setup = {"mcpServers": {"geko": {"command": "geko", "args": ["mcp"], "env": {
                "GEKO_DB_PATH": str(root / "data" / "geko.db"),
                "GEKO_CONFIG": str(root / "elsewhere.yaml"),
            }}}}
            with self.assertRaisesRegex(RuntimeError, "config path"):
                smoke.validate_setup(setup, root)

    def test_accepts_released_command_with_both_project_paths_pinned(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            setup = {"mcpServers": {"geko": {"command": "geko", "args": ["mcp"], "env": {
                "GEKO_DB_PATH": str(root / "data" / "geko.db"),
                "GEKO_CONFIG": str(root / "geko.yaml"),
            }}}}
            smoke.validate_setup(setup, root)

    def test_rejects_relative_database_path(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            setup = {"mcpServers": {"geko": {"env": {
                "GEKO_DB_PATH": "data/geko.db", "GEKO_CONFIG": str(root / "geko.yaml"),
            }}}}
            with self.assertRaisesRegex(RuntimeError, "database path"):
                smoke.validate_setup(setup, root)


if __name__ == "__main__":
    unittest.main()
