class Geko < Formula
  desc "Local-first OpenTelemetry trace debugger: find the failure, follow the trace"
  homepage "https://github.com/YunusEmreAlps/geko"
  version "0.1.0"
  license "MIT"

  on_macos do
    on_arm do
      url "https://github.com/YunusEmreAlps/geko/releases/download/v0.1.0/geko_0.1.0_darwin_arm64.tar.gz"
      sha256 "b4c53792445e562bd2232f17af092ed8e8ba02ce7130e26d5a1a4f49f388feaf"
    end
    on_intel do
      url "https://github.com/YunusEmreAlps/geko/releases/download/v0.1.0/geko_0.1.0_darwin_amd64.tar.gz"
      sha256 "51be893bbff63a0b5a33d7e798ae93f2bd17e201919e2710895326c381567f54"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/YunusEmreAlps/geko/releases/download/v0.1.0/geko_0.1.0_linux_arm64.tar.gz"
      sha256 "ee5e936875516780df52a401791cbc1daf705a429b6cfde727951e5767ce862e"
    end
    on_intel do
      url "https://github.com/YunusEmreAlps/geko/releases/download/v0.1.0/geko_0.1.0_linux_amd64.tar.gz"
      sha256 "99207c19f648224befac878aaad995dd4194e789d8c49dce8336615e36b3ac87"
    end
  end

  def install
    bin.install "geko"
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/geko version")
  end
end
