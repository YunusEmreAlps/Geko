"""Check an already installed release locally; does not publish or install anything."""
import argparse
import json
import os
import platform
from pathlib import Path
import socket
import subprocess
import tempfile
import time
import urllib.request


def validate_setup(setup, root):
    pinned = setup["mcpServers"]["geko"]["env"]
    if Path(pinned["GEKO_DB_PATH"]) != root / "data" / "geko.db":
        raise RuntimeError("MCP database path was not pinned")
    if Path(pinned.get("GEKO_CONFIG", "")) != root / "geko.yaml":
        raise RuntimeError("MCP config path was not pinned")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("binary", type=Path)
    parser.add_argument("--version", required=True)
    parser.add_argument("--output", type=Path, help="also save machine-readable evidence")
    args = parser.parse_args()
    binary = str(args.binary.resolve())
    version = subprocess.check_output([binary, "version"], text=True, timeout=10)
    if not version.startswith(f"Geko {args.version} "):
        raise RuntimeError("unexpected release version")
    with tempfile.TemporaryDirectory(prefix="geko-release-smoke-") as directory:
        root = Path(directory)
        (root / "geko.yaml").write_text("db_path: data/geko.db\n", encoding="utf-8")
        env = {key: value for key, value in os.environ.items() if not key.startswith("GEKO_")}
        setup = json.loads(subprocess.check_output(
            [binary, "mcp", "setup", "cursor", "--print"], cwd=root, env=env, text=True, timeout=10))
        validate_setup(setup, root)
        with socket.socket() as listener:
            listener.bind(("127.0.0.1", 0))
            port = listener.getsockname()[1]
        base = f"http://127.0.0.1:{port}"
        with (root / "demo.log").open("w+", encoding="utf-8") as log:
            child = subprocess.Popen([binary, "demo", "--no-open", "--listen", f"127.0.0.1:{port}"],
                cwd=root, env=env, stdout=log, stderr=log,
                creationflags=subprocess.CREATE_NO_WINDOW if os.name == "nt" else 0)
            try:
                deadline = time.monotonic() + 120
                while True:
                    if child.poll() is not None:
                        log.seek(0)
                        raise RuntimeError(f"demo exited: {log.read()}")
                    try:
                        with urllib.request.urlopen(base + "/readyz", timeout=2) as response:
                            if response.status == 204:
                                break
                    except OSError:
                        pass
                    if time.monotonic() >= deadline:
                        raise RuntimeError("demo readiness timed out")
                    time.sleep(0.2)
                with urllib.request.urlopen(base + "/api/v1/failures?limit=1", timeout=5) as response:
                    failures = json.load(response)
                if not failures.get("items"):
                    raise RuntimeError("release demo has no seeded failure evidence")
                with urllib.request.urlopen(base + "/", timeout=5) as response:
                    if response.status != 200 or b"<html" not in response.read().lower():
                        raise RuntimeError("release dashboard did not return HTML")
                result = {"version": args.version, "version_output": version.strip(),
                    "platform": platform.system(), "machine": platform.machine(),
                    "demo_ready": True, "seeded_failures": True, "mcp_absolute_paths": True,
                    "dashboard": True, "mcp_command": setup["mcpServers"]["geko"]["command"],
                    "mcp_protocol_verified": False, "hosted_action_verified": False}
                encoded = json.dumps(result, indent=2) + "\n"
                if args.output:
                    args.output.write_text(encoded, encoding="utf-8")
                print(encoded, end="")
            finally:
                child.terminate()
                child.wait(timeout=15)


if __name__ == "__main__":
    main()
