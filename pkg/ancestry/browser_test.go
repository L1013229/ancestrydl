package ancestry

import (
	"testing"

	"github.com/go-rod/rod/lib/launcher/flags"
)

// The fork's headless adaptation (commit 46862fc) is two launcher flags. An
// upstream sync that drops either leaves a binary that cannot start a
// browser on the server, and nothing else in the suite launches one.
func TestServerLauncherIsHeadlessWithoutSandbox(t *testing.T) {
	l := serverLauncher()
	if !l.Has(flags.Headless) {
		t.Fatal("server launcher must run headless: the server has no display")
	}
	if !l.Has(flags.NoSandbox) {
		t.Fatal("server launcher must disable the Chromium sandbox: it is unavailable on the server")
	}
}
