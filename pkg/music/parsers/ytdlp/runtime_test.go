package ytdlp

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// resetRuntimeDetection lets each case start from a clean slate, since the
// detection is deliberately done once per process.
func resetRuntimeDetection(t *testing.T, found map[string]bool) {
	t.Helper()
	// sync.Once carries a noCopy, so it is reset to a fresh zero value rather
	// than saved and restored.
	origLook, origOpts, origCookies := lookPath, youtubeOpts, cookiesPath
	t.Cleanup(func() {
		lookPath, youtubeOpts, cookiesPath = origLook, origOpts, origCookies
		runtimeOnce = sync.Once{}
	})

	runtimeOnce = sync.Once{}
	youtubeOpts = nil
	// Cookies are off unless a case asks for them, so the order cases run in
	// does not change what any of them assert.
	SetCookiesPath("")
	lookPath = func(name string) (string, error) {
		if found[name] {
			return "/usr/bin/" + name, nil
		}
		return "", errors.New("not found")
	}
}

func TestYoutubeArgsPrefersDeno(t *testing.T) {
	// deno is the one runtime yt-dlp enables by itself, so it is preferred where
	// several are installed.
	resetRuntimeDetection(t, map[string]bool{"deno": true, "node": true, "bun": true})

	got := youtubeArgs()
	if i := slices.Index(got, "--js-runtimes"); i < 0 || got[i+1] != "deno" {
		t.Fatalf("args = %v, want deno", got)
	}
	if i := slices.Index(got, "--extractor-args"); i < 0 || got[i+1] != embeddedWebClient {
		t.Fatalf("args = %v, want the embedded web client", got)
	}
}

func TestYoutubeArgsFallsDownTheList(t *testing.T) {
	resetRuntimeDetection(t, map[string]bool{"node": true, "bun": true})

	got := youtubeArgs()
	if i := slices.Index(got, "--js-runtimes"); i < 0 || got[i+1] != "node" {
		t.Fatalf("args = %v, want node", got)
	}
}

// The important negative case: asking for the embedded web client without a
// runtime fails outright rather than degrading, so no runtime must mean no
// flags — a partial failure (live streams) beats a total one (nothing plays).
func TestYoutubeArgsEmptyWithoutARuntime(t *testing.T) {
	resetRuntimeDetection(t, nil)

	if got := youtubeArgs(); len(got) != 0 {
		t.Fatalf("args = %v, want none without a JS runtime", got)
	}
}

func TestYoutubeArgsDetectsOnce(t *testing.T) {
	resetRuntimeDetection(t, map[string]bool{"node": true})

	calls := 0
	inner := lookPath
	lookPath = func(name string) (string, error) {
		calls++
		return inner(name)
	}

	for i := 0; i < 5; i++ {
		youtubeArgs()
	}
	// deno misses, node hits: two lookups, once.
	if calls != 2 {
		t.Fatalf("lookPath called %d times, want the detection to happen once", calls)
	}
}

// The age-gate case: a configured cookie file must reach yt-dlp, and must reach
// it as a writable copy rather than as the configured path, which is typically
// a read-only mount that yt-dlp cannot refresh in place.
func TestYoutubeArgsUsesAWritableCookieCopy(t *testing.T) {
	resetRuntimeDetection(t, map[string]bool{"node": true})

	src := filepath.Join(t.TempDir(), "cookies.txt")
	const body = "# Netscape HTTP Cookie File\n.youtube.com\tTRUE\t/\tTRUE\t0\tSID\tvalue\n"
	if err := os.WriteFile(src, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	SetCookiesPath(src)

	got := youtubeArgs()
	i := slices.Index(got, "--cookies")
	if i < 0 {
		t.Fatalf("args = %v, want --cookies", got)
	}
	dst := got[i+1]
	if dst == src {
		t.Fatal("yt-dlp was pointed at the configured path; it rewrites the jar in place and the mount is read-only")
	}
	copied, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("copy unreadable: %v", err)
	}
	if string(copied) != body {
		t.Fatalf("copy = %q, want the source contents", copied)
	}
}

// A bad path must degrade to anonymous extraction rather than break YouTube
// entirely — everything that is not gated still plays.
func TestYoutubeArgsSurvivesAnUnreadableCookieFile(t *testing.T) {
	resetRuntimeDetection(t, map[string]bool{"node": true})
	SetCookiesPath(filepath.Join(t.TempDir(), "absent.txt"))

	got := youtubeArgs()
	if slices.Contains(got, "--cookies") {
		t.Fatalf("args = %v, want no --cookies for an unreadable file", got)
	}
	if !slices.Contains(got, "--js-runtimes") {
		t.Fatalf("args = %v, want extraction to carry on anonymously", got)
	}
}

// Cookies do not depend on the JS runtime probe: authentication is what reaches
// a gated video, with or without a runtime to drive the embedded client.
func TestCookiesApplyWithoutAJSRuntime(t *testing.T) {
	resetRuntimeDetection(t, nil)

	src := filepath.Join(t.TempDir(), "cookies.txt")
	if err := os.WriteFile(src, []byte("# Netscape HTTP Cookie File\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	SetCookiesPath(src)

	got := youtubeArgs()
	if !slices.Contains(got, "--cookies") {
		t.Fatalf("args = %v, want --cookies even with no runtime", got)
	}
	if slices.Contains(got, "--js-runtimes") {
		t.Fatalf("args = %v, want no runtime flags when none is installed", got)
	}
}

func TestArgsCopiesThePrefix(t *testing.T) {
	resetRuntimeDetection(t, map[string]bool{"node": true})

	first := args("-j", "https://a.test/1")
	second := args("-o", "-", "https://a.test/2")

	// Appending into the shared prefix would have let the second call rewrite
	// the first one's arguments.
	if slices.Contains(first, "https://a.test/2") {
		t.Fatalf("first invocation was scribbled on: %v", first)
	}
	if !slices.Contains(first, "https://a.test/1") || !slices.Contains(second, "https://a.test/2") {
		t.Fatalf("first=%v second=%v", first, second)
	}
	if !strings.HasPrefix(strings.Join(second, " "), "--js-runtimes node") {
		t.Fatalf("options must lead: %v", second)
	}
}
