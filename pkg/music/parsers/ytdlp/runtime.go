package ytdlp

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// yt-dlp's own defaults are wrong for this project, and the difference only
// shows on live streams — which is exactly why it stayed hidden. Left alone,
// yt-dlp falls back to YouTube's android_vr client, and googlevideo serves that
// client under restrictions: on a live broadcast its segments start answering
// 403 after twenty-odd seconds and playback stops. It is the same client this
// project already moved off in ytnative, for the same reason.
//
// The fix is two flags, and they are passed on every invocation rather than
// written to a config file. A file would have to live somewhere: the user's own
// yt-dlp config is shared with everything else they run, and a generated one
// passed with --config-location suppresses every other config yt-dlp would have
// loaded, including the container's. Arguments have neither problem, cannot go
// stale against the code that depends on them, and keep the bot from writing
// files at runtime.
const (
	// embeddedWebClient avoids the formats that need a PO token, which is what
	// the container's config has always said in its comment.
	//
	// web_embedded leads because of the live-stream reason above, but it must not
	// be the only client: it is served by the embed endpoint, so a video whose
	// uploader disabled embedding refuses it outright. tv and mweb are the two
	// clients yt-dlp falls back on for gated content, and naming them costs
	// nothing when the first client works — yt-dlp walks the list in order and
	// stops at the first that yields formats.
	embeddedWebClient = "youtube:player_client=web_embedded,tv,mweb"
)

// cookiesPath is the file holding YouTube cookies in Netscape format. Empty
// means anonymous extraction, which is the default and handles almost
// everything; a path switches yt-dlp to authenticated requests, which is the
// only way to reach age-gated videos. Set once at startup via SetCookiesPath,
// before any playback, like the other cross-package playback settings.
var cookiesPath string

// SetCookiesPath configures the YouTube cookie file. Call before first use;
// detection is cached for the life of the process.
func SetCookiesPath(path string) { cookiesPath = strings.TrimSpace(path) }

// cookieArgs returns --cookies pointing at a writable copy of the configured
// file, or nil when none is configured or it cannot be read.
//
// The copy is the point. yt-dlp rewrites the cookie jar in place as YouTube
// refreshes the session, so it needs somewhere writable; the configured path
// usually is not, because the natural way to deliver cookies to a container is
// a read-only mount. Worse, a PaaS file mount re-materialises its stored
// content on every redeploy, so writes that did land would be reverted anyway.
// Copying once at startup means yt-dlp refreshes the copy for the life of the
// process and re-reads the mount on restart, which is the behaviour a mounted
// secret should have.
func cookieArgs() []string {
	path := cookiesPath
	if path == "" {
		return nil
	}

	l := logger()
	data, err := os.ReadFile(path)
	if err != nil {
		// Not fatal: anonymous extraction still plays everything that is not
		// gated, so a bad path degrades rather than taking YouTube down with it.
		l.Warn().Str("path", path).Err(err).Msg("ytdlp_cookies_unreadable")
		return nil
	}

	dst := filepath.Join(os.TempDir(), "melodix-ytdlp-cookies.txt")
	if err := os.WriteFile(dst, data, 0o600); err != nil {
		l.Warn().Str("path", dst).Err(err).Msg("ytdlp_cookies_copy_failed")
		return nil
	}

	l.Info().Str("source", path).Str("using", dst).Msg("ytdlp_cookies_configured")
	return []string{"--cookies", dst}
}

// jsRuntimeCandidates are the runtimes yt-dlp can drive, in preference order.
// Only deno is enabled by default; the rest need naming explicitly.
var jsRuntimeCandidates = []string{"deno", "node", "bun"}

// lookPath is a seam for tests.
var lookPath = exec.LookPath

var (
	runtimeOnce sync.Once
	youtubeOpts []string
)

// youtubeArgs returns the extra arguments for a yt-dlp invocation, or nil when
// the environment cannot support them.
//
// The embedded web client cannot solve YouTube's challenges without a
// JavaScript runtime — it fails outright rather than degrading — so asking for
// it where none is installed would trade a partial failure (live streams) for a
// total one (nothing plays). Where no runtime is found this returns nil and
// yt-dlp keeps its own defaults.
func youtubeArgs() []string {
	runtimeOnce.Do(func() {
		youtubeOpts = append(youtubeOpts, jsRuntimeArgs()...)
		// Cookies are resolved independently of the runtime probe: authentication
		// is what reaches an age-gated video, and it works whether or not a JS
		// runtime is present to drive the embedded client.
		youtubeOpts = append(youtubeOpts, cookieArgs()...)
	})
	return youtubeOpts
}

// jsRuntimeArgs returns the runtime and player-client flags, or nil when no
// runtime is installed.
func jsRuntimeArgs() []string {
	for _, rt := range jsRuntimeCandidates {
		if _, err := lookPath(rt); err != nil {
			continue
		}
		l := logger()
		l.Info().Str("js_runtime", rt).Str("player_client", embeddedWebClient).
			Msg("ytdlp_youtube_client_configured")
		return []string{"--js-runtimes", rt, "--extractor-args", embeddedWebClient}
	}
	l := logger()
	l.Warn().Strs("looked_for", jsRuntimeCandidates).
		Msg("ytdlp_no_js_runtime_live_streams_will_fail")
	return nil
}

// args builds a full yt-dlp argument list, prefixing the YouTube options.
// The prefix is copied rather than appended to: youtubeArgs returns the shared
// slice, and appending into it would let one invocation scribble on the next.
func args(rest ...string) []string {
	opts := youtubeArgs()
	out := make([]string, 0, len(opts)+len(rest))
	out = append(out, opts...)
	return append(out, rest...)
}
