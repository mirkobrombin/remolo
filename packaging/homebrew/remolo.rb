# Homebrew formula for remolo. Replace VERSION and the sha256 values at release
# time (the release workflow can template this), or use as a tap.
class Remolo < Formula
  desc "Reach a shell, files and the screen of another machine with one token"
  homepage "https://github.com/mirkobrombin/remolo"
  version "0.1.0"
  license "MIT"

  on_macos do
    on_arm do
      url "https://github.com/mirkobrombin/remolo/releases/download/v#{version}/remolo-darwin-arm64"
      sha256 "REPLACE_WITH_SHA256"
    end
    on_intel do
      url "https://github.com/mirkobrombin/remolo/releases/download/v#{version}/remolo-darwin-amd64"
      sha256 "REPLACE_WITH_SHA256"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/mirkobrombin/remolo/releases/download/v#{version}/remolo-linux-arm64"
      sha256 "REPLACE_WITH_SHA256"
    end
    on_intel do
      url "https://github.com/mirkobrombin/remolo/releases/download/v#{version}/remolo-linux-amd64"
      sha256 "REPLACE_WITH_SHA256"
    end
  end

  def install
    bin.install Dir["remolo-*"].first => "remolo"
  end

  test do
    assert_match "REMOLO1", shell_output("#{bin}/remolo token REMOLO1-XXXX 2>&1", 1)
  end
end
