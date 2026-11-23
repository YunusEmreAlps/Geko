class Geko < Formula
  desc "Local-first OpenTelemetry trace debugger: find the failure, follow the trace"
  homepage "https://github.com/YunusEmreAlps/geko"
  version "0.2.0"
  license "MIT"

  on_macos do
    on_arm do
      url "https://github.com/YunusEmreAlps/geko/releases/download/v0.2.0/geko_0.2.0_darwin_arm64.tar.gz"
      sha256 "86d45d53a9fbdcb583e17da338a0e52b6cc7e4a31bb1034a1f8e87826d8bb8d5"
    end
    on_intel do
      url "https://github.com/YunusEmreAlps/geko/releases/download/v0.2.0/geko_0.2.0_darwin_amd64.tar.gz"
      sha256 "821bca5d5ff5d79bd18978f0fd0ad20d288514332cd7ff5b48e2ecc0d3748e7a"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/YunusEmreAlps/geko/releases/download/v0.2.0/geko_0.2.0_linux_arm64.tar.gz"
      sha256 "aff0318377a06f85e812a95b7097296cfb7dbdec62b7f7705b242d8aef22fd16"
    end
    on_intel do
      url "https://github.com/YunusEmreAlps/geko/releases/download/v0.2.0/geko_0.2.0_linux_amd64.tar.gz"
      sha256 "f0e23436ea1dcc7ac271e0ae59e6eb36c0aba0fcb8dba9005fef6a25ae7f316f"
    end
  end

  def install
    bin.install "geko"
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/geko version")
  end
end
