"""Write the Scoop manifest and Homebrew formula for a published release.

    python scripts/update-packages.py 0.1.0

Both read the archive checksums from the release's checksums.txt, so run this
after the release workflow has published it, then commit bucket/geko.json and
Formula/geko.rb. Scoop can also update its manifest itself from checksums.txt.
"""

import argparse
import json
import re
import urllib.request
from pathlib import Path

REPO = "YunusEmreAlps/geko"
ROOT = Path(__file__).resolve().parents[1]
DESCRIPTION = "Local-first OpenTelemetry trace debugger: find the failure, follow the trace"


def checksums(version):
    url = f"https://github.com/{REPO}/releases/download/v{version}/checksums.txt"
    with urllib.request.urlopen(url, timeout=30) as response:
        lines = response.read().decode().splitlines()
    sums = {}
    for line in lines:
        digest, name = line.split()
        if not re.fullmatch(r"[0-9a-f]{64}", digest):
            raise SystemExit(f"unexpected line in checksums.txt: {line!r}")
        sums[name] = digest
    return sums


def archive(version, os_name, arch):
    extension = "zip" if os_name == "windows" else "tar.gz"
    return f"geko_{version}_{os_name}_{arch}.{extension}"


def scoop(version, sums):
    base = f"https://github.com/{REPO}/releases/download/v{version}"
    template = f"https://github.com/{REPO}/releases/download/v$version"
    architectures = {"64bit": "amd64", "arm64": "arm64"}
    return {
        "version": version,
        "description": DESCRIPTION,
        "homepage": f"https://github.com/{REPO}",
        "license": "MIT",
        "architecture": {
            key: {"url": f"{base}/{archive(version, 'windows', arch)}", "hash": sums[archive(version, "windows", arch)]}
            for key, arch in architectures.items()
        },
        "bin": "geko.exe",
        "checkver": "github",
        "autoupdate": {
            "architecture": {
                key: {"url": f"{template}/geko_$version_windows_{arch}.zip"} for key, arch in architectures.items()
            },
            "hash": {"url": "$baseurl/checksums.txt"},
        },
    }


def homebrew(version, sums):
    base = f"https://github.com/{REPO}/releases/download/v{version}"

    def block(os_name, arch):
        name = archive(version, os_name, arch)
        return f'      url "{base}/{name}"\n      sha256 "{sums[name]}"'

    return f'''class Geko < Formula
  desc "{DESCRIPTION}"
  homepage "https://github.com/{REPO}"
  version "{version}"
  license "MIT"

  on_macos do
    on_arm do
{block("darwin", "arm64")}
    end
    on_intel do
{block("darwin", "amd64")}
    end
  end

  on_linux do
    on_arm do
{block("linux", "arm64")}
    end
    on_intel do
{block("linux", "amd64")}
    end
  end

  def install
    bin.install "geko"
  end

  test do
    assert_match version.to_s, shell_output("#{{bin}}/geko version")
  end
end
'''


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("version", help="release version without the leading v, such as 0.1.0")
    version = parser.parse_args().version.removeprefix("v")
    sums = checksums(version)
    bucket = ROOT / "bucket" / "geko.json"
    formula = ROOT / "Formula" / "geko.rb"
    bucket.parent.mkdir(exist_ok=True)
    formula.parent.mkdir(exist_ok=True)
    bucket.write_text(json.dumps(scoop(version, sums), indent=4) + "\n", encoding="utf-8", newline="\n")
    formula.write_text(homebrew(version, sums), encoding="utf-8", newline="\n")
    print(f"wrote {bucket.relative_to(ROOT)} and {formula.relative_to(ROOT)} for {version}")


if __name__ == "__main__":
    main()
