#
# Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
# Date: 2026-06-22
#

# Homebrew formula for the Harbor CLI. Downloads the pre-built binary for the
# current platform/architecture from the latest GitHub release. The version
# line is updated automatically by the release workflow.
class Harbor < Formula
  desc "Command-line client for the Harbor notes API"
  homepage "https://github.com/HarborMyNotes/harbor-cli"
  license "MIT"
  version "0.1.26"

  if OS.mac? && Hardware::CPU.arm?
    url "https://github.com/HarborMyNotes/harbor-cli/releases/latest/download/harbor-darwin-arm64"
  elsif OS.mac? && Hardware::CPU.intel?
    url "https://github.com/HarborMyNotes/harbor-cli/releases/latest/download/harbor-darwin-amd64"
  elsif OS.linux? && Hardware::CPU.arm?
    url "https://github.com/HarborMyNotes/harbor-cli/releases/latest/download/harbor-linux-arm64"
  elsif OS.linux? && Hardware::CPU.intel?
    url "https://github.com/HarborMyNotes/harbor-cli/releases/latest/download/harbor-linux-amd64"
  end

  def install
    binary_name = stable.url.split("/").last
    bin.install binary_name => "harbor"
  end

  test do
    assert_match "harbor version", shell_output("#{bin}/harbor --version")
  end
end
