package ytdl

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// minimalEJSScript is a minimal stub that implements the jsc() interface used
// by the real EJS core script.  It is used in unit tests to avoid requiring
// the real yt.solver.core.js file.
//
// For n-challenges it simply reverses the input string as a predictable
// transformation; for sig-challenges it returns the input unchanged.
const minimalEJSScript = `
var jsc = (function(meriyah, astring) {
    function main(input) {
        var player = input.type === 'player' ? input.player : input.preprocessed_player;
        var responses = input.requests.map(function(req) {
            if (req.type !== 'n' && req.type !== 'sig') {
                return { type: 'error', error: 'unknown type: ' + req.type };
            }
            var data = {};
            req.challenges.forEach(function(c) {
                // Stub: reverse the string as a fake transformation.
                data[c] = c.split('').reverse().join('');
            });
            return { type: 'result', data: data };
        });
        var out = { type: 'result', responses: responses };
        if (input.type === 'player' && input.output_preprocessed) {
            out.preprocessed_player = { stub: true };
        }
        return out;
    }
    return main;
})(meriyah, astring);
`

// buildTestDownloader creates a [Downloader] using the minimal stub EJS
// script written to a temporary file.  The provided HTTP handler is used to
// serve all requests made by the Downloader.
func buildTestDownloader(t *testing.T, handler http.Handler) *Downloader {
	t.Helper()

	// Write the stub EJS script to a temp file.
	dir := t.TempDir()
	ejsPath := filepath.Join(dir, "yt.solver.core.js")
	if err := os.WriteFile(ejsPath, []byte(minimalEJSScript), 0o600); err != nil {
		t.Fatalf("write stub EJS: %v", err)
	}

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	opts := Options{
		HTTPClient: srv.Client(),
	}

	// Redirect all outbound requests to the test server by replacing the
	// Transport with one that rewrites the host.
	transport := &rewriteTransport{base: srv.Client().Transport, target: srv.URL}
	opts.HTTPClient.Transport = transport

	dl, err := NewDownloaderWithOptions(ejsPath, opts)
	if err != nil {
		t.Fatalf("NewDownloaderWithOptions: %v", err)
	}
	return dl
}

// rewriteTransport rewrites all request URLs to the target server so that
// integration tests can intercept external HTTP calls.
type rewriteTransport struct {
	base   http.RoundTripper
	target string
}

func (rt *rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	parsed, _ := url.Parse(rt.target)
	clone := req.Clone(req.Context())
	clone.URL.Scheme = parsed.Scheme
	clone.URL.Host = parsed.Host
	// Reset the Host header so the test server receives the request.
	clone.Host = parsed.Host
	base := rt.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(clone)
}

// -----------------------------------------------------------------------
// Unit tests for internal helpers
// -----------------------------------------------------------------------

func TestCollectFormats_VideoOnly(t *testing.T) {
	pr := &playerResponse{}
	pr.StreamingData.AdaptiveFormats = []streamFormat{
		{URL: "https://example.com/v1", MimeType: "video/mp4", Bitrate: 1_000_000, Height: 720},
		{URL: "https://example.com/a1", MimeType: "audio/mp4", Bitrate: 128_000},
		{URL: "https://example.com/v2", MimeType: "video/webm", Bitrate: 2_000_000, Height: 1080},
		// No URL and no signatureCipher – should be skipped.
		{URL: "", MimeType: "video/mp4", Bitrate: 500_000, Height: 480},
		// Has signatureCipher but no direct URL – should be included.
		{SignatureCipher: "url=https%3A%2F%2Fexample.com%2Fv3&s=CIPHER&sp=sig", MimeType: "video/mp4", Bitrate: 1_500_000, Height: 720},
	}

	fmts := collectFormats(pr, MediaTypeVideo)
	if len(fmts) != 3 {
		t.Fatalf("expected 3 video formats, got %d", len(fmts))
	}
	for _, f := range fmts {
		if !strings.HasPrefix(strings.ToLower(f.MimeType), "video/") {
			t.Errorf("non-video MIME type in result: %q", f.MimeType)
		}
	}
}

func TestCollectFormats_AudioOnly(t *testing.T) {
	pr := &playerResponse{}
	pr.StreamingData.AdaptiveFormats = []streamFormat{
		{URL: "https://example.com/v1", MimeType: "video/mp4", Bitrate: 1_000_000, Height: 720},
		{URL: "https://example.com/a1", MimeType: "audio/mp4", Bitrate: 128_000},
		{URL: "https://example.com/a2", MimeType: "audio/webm", Bitrate: 256_000},
	}

	fmts := collectFormats(pr, MediaTypeAudio)
	if len(fmts) != 2 {
		t.Fatalf("expected 2 audio formats, got %d", len(fmts))
	}
	for _, f := range fmts {
		if !strings.HasPrefix(strings.ToLower(f.MimeType), "audio/") {
			t.Errorf("non-audio MIME type in result: %q", f.MimeType)
		}
	}
}

func TestSortFormats_Video(t *testing.T) {
	fmts := []internalFormat{
		{URL: "low", Height: 360, Bitrate: 300_000},
		{URL: "high", Height: 1080, Bitrate: 2_000_000},
		{URL: "mid", Height: 720, Bitrate: 1_000_000},
	}
	sortFormats(fmts, MediaTypeVideo)
	if fmts[0].URL != "high" {
		t.Errorf("expected 'high' first, got %q", fmts[0].URL)
	}
	if fmts[1].URL != "mid" {
		t.Errorf("expected 'mid' second, got %q", fmts[1].URL)
	}
}

func TestSortFormats_Audio(t *testing.T) {
	fmts := []internalFormat{
		{URL: "low", Bitrate: 64_000},
		{URL: "high", Bitrate: 320_000},
		{URL: "mid", Bitrate: 128_000},
	}
	sortFormats(fmts, MediaTypeAudio)
	if fmts[0].URL != "high" {
		t.Errorf("expected 'high' first, got %q", fmts[0].URL)
	}
}

// -----------------------------------------------------------------------
// Unit test for the goja-based EJS wrapper
// -----------------------------------------------------------------------

func TestNewDownloader_LoadsEJSScript(t *testing.T) {
	dir := t.TempDir()
	ejsPath := filepath.Join(dir, "yt.solver.core.js")
	if err := os.WriteFile(ejsPath, []byte(minimalEJSScript), 0o600); err != nil {
		t.Fatalf("write stub EJS: %v", err)
	}

	dl, err := NewDownloader(ejsPath)
	if err != nil {
		t.Fatalf("NewDownloader: %v", err)
	}
	if dl == nil {
		t.Fatal("NewDownloader returned nil Downloader")
	}
}

func TestNewDownloader_MissingScript(t *testing.T) {
	_, err := NewDownloader("/tmp/does_not_exist_ytdl_test.js")
	if err == nil {
		t.Fatal("expected error for missing EJS script, got nil")
	}
}

func TestNewDownloader_InvalidScript(t *testing.T) {
	dir := t.TempDir()
	ejsPath := filepath.Join(dir, "bad.js")
	if err := os.WriteFile(ejsPath, []byte("this is not valid JS {{{"), 0o600); err != nil {
		t.Fatalf("write bad JS: %v", err)
	}
	_, err := NewDownloader(ejsPath)
	if err == nil {
		t.Fatal("expected error for invalid JS, got nil")
	}
}

func TestRunEJS_NChallengeStub(t *testing.T) {
	dir := t.TempDir()
	ejsPath := filepath.Join(dir, "yt.solver.core.js")
	if err := os.WriteFile(ejsPath, []byte(minimalEJSScript), 0o600); err != nil {
		t.Fatalf("write stub EJS: %v", err)
	}

	dl, err := NewDownloader(ejsPath)
	if err != nil {
		t.Fatalf("NewDownloader: %v", err)
	}

	const challenge = "ABCxyz"
	results, err := dl.runEJS("https://example.com/player.js", "// fake player JS", "n", []string{challenge})
	if err != nil {
		t.Fatalf("runEJS: %v", err)
	}

	got, ok := results[challenge]
	if !ok {
		t.Fatalf("no result for challenge %q; got %v", challenge, results)
	}

	// The stub reverses the input.
	want := "zyxCBA"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// -----------------------------------------------------------------------
// Integration-style tests using a mock HTTP server
// -----------------------------------------------------------------------

// mockHandler returns an http.Handler that serves a minimal YouTube API
// environment, including the player response and player JavaScript.
func mockHandler(t *testing.T, videoID string, hasNParam bool) http.Handler {
	t.Helper()

	nValue := "AAABBBCCC"
	streamURL := "https://rr1.example.com/videoplayback?expire=99999&itag=137"
	if hasNParam {
		streamURL += "&n=" + nValue
	}

	playerResponse := map[string]interface{}{
		"playabilityStatus": map[string]interface{}{
			"status": "OK",
		},
		"streamingData": map[string]interface{}{
			"adaptiveFormats": []map[string]interface{}{
				{
					"itag":           137,
					"url":            streamURL,
					"mimeType":       "video/mp4; codecs=\"avc1.640028\"",
					"bitrate":        3_000_000,
					"averageBitrate": 2_800_000,
					"width":          1920,
					"height":         1080,
					"quality":        "hd1080",
					"qualityLabel":   "1080p",
				},
				{
					"itag":           140,
					"url":            "https://rr1.example.com/videoplayback?expire=99999&itag=140",
					"mimeType":       "audio/mp4; codecs=\"mp4a.40.2\"",
					"bitrate":        128_000,
					"averageBitrate": 128_000,
					"audioQuality":   "AUDIO_QUALITY_MEDIUM",
				},
			},
		},
	}

	playerResponseJSON, _ := json.Marshal(playerResponse)

	mux := http.NewServeMux()

	// InnerTube player endpoint.
	mux.HandleFunc("/youtubei/v1/player", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(playerResponseJSON)
	})

	// YouTube watch page – contains a player JS URL.
	mux.HandleFunc("/watch", func(w http.ResponseWriter, r *http.Request) {
		page := `<html><body>
			<script>var ytcfg = {"PLAYER_JS_URL":"/s/player/abc12345/player_ias.vflset/en_US/base.js"};</script>
			</body></html>`
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(page))
	})

	// Player JavaScript.
	mux.HandleFunc("/s/player/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		_, _ = w.Write([]byte("// fake player JS"))
	})

	return mux
}

func TestGetURL_VideoNoNParam(t *testing.T) {
	dl := buildTestDownloader(t, mockHandler(t, "testVideoID", false))

	got, err := dl.GetURL("testVideoID", MediaTypeVideo)
	if err != nil {
		t.Fatalf("GetURL: %v", err)
	}
	if !strings.Contains(got, "itag=137") {
		t.Errorf("expected video itag=137 in URL, got %q", got)
	}
}

func TestGetURL_AudioNoNParam(t *testing.T) {
	dl := buildTestDownloader(t, mockHandler(t, "testVideoID", false))

	got, err := dl.GetURL("testVideoID", MediaTypeAudio)
	if err != nil {
		t.Fatalf("GetURL: %v", err)
	}
	if !strings.Contains(got, "itag=140") {
		t.Errorf("expected audio itag=140 in URL, got %q", got)
	}
}

func TestGetURL_VideoWithNParam(t *testing.T) {
	dl := buildTestDownloader(t, mockHandler(t, "testVideoID", true))

	got, err := dl.GetURL("testVideoID", MediaTypeVideo)
	if err != nil {
		t.Fatalf("GetURL: %v", err)
	}

	// The stub EJS reverses the n parameter: "AAABBBCCC" → "CCCBBBAAA".
	// Ensure the original value is gone.
	if strings.Contains(got, "n=AAABBBCCC") {
		t.Errorf("n parameter was not transformed; got URL %q", got)
	}
	// Ensure an n parameter is still present (with the transformed value).
	if !strings.Contains(got, "n=") {
		t.Errorf("n parameter disappeared from URL %q", got)
	}
}

func TestGetURL_EmptyVideoID(t *testing.T) {
	dir := t.TempDir()
	ejsPath := filepath.Join(dir, "yt.solver.core.js")
	_ = os.WriteFile(ejsPath, []byte(minimalEJSScript), 0o600)
	dl, _ := NewDownloader(ejsPath)

	_, err := dl.GetURL("", MediaTypeVideo)
	if err == nil {
		t.Fatal("expected error for empty videoID, got nil")
	}
}

func TestGetURL_InvalidMediaType(t *testing.T) {
	dir := t.TempDir()
	ejsPath := filepath.Join(dir, "yt.solver.core.js")
	_ = os.WriteFile(ejsPath, []byte(minimalEJSScript), 0o600)
	dl, _ := NewDownloader(ejsPath)

	_, err := dl.GetURL("dQw4w9WgXcQ", "unknown")
	if err == nil {
		t.Fatal("expected error for invalid media type, got nil")
	}
}

func TestGetURL_UnavailableVideo(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/youtubei/v1/player", func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{
			"playabilityStatus": map[string]interface{}{
				"status": "ERROR",
				"reason": "Video unavailable",
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	dl := buildTestDownloader(t, mux)
	_, err := dl.GetURL("badVideoID", MediaTypeVideo)
	if err == nil {
		t.Fatal("expected error for unavailable video, got nil")
	}
}

// -----------------------------------------------------------------------
// Unit tests for sig-cipher helpers
// -----------------------------------------------------------------------

func TestParseSignatureCipher_Valid(t *testing.T) {
	rawURL := "https://rr1.example.com/videoplayback?expire=99999&itag=137"
	sc := "url=" + url.QueryEscape(rawURL) + "&s=CIPHERSIG&sp=sig"

	psc, err := parseSignatureCipher(sc)
	if err != nil {
		t.Fatalf("parseSignatureCipher: %v", err)
	}
	if psc.streamURL != rawURL {
		t.Errorf("streamURL: got %q, want %q", psc.streamURL, rawURL)
	}
	if psc.cipher != "CIPHERSIG" {
		t.Errorf("cipher: got %q, want %q", psc.cipher, "CIPHERSIG")
	}
	if psc.sigParam != "sig" {
		t.Errorf("sigParam: got %q, want %q", psc.sigParam, "sig")
	}
}

func TestParseSignatureCipher_DefaultSigParam(t *testing.T) {
	// When "sp" is absent the default "sig" should be used.
	rawURL := "https://rr1.example.com/videoplayback"
	sc := "url=" + url.QueryEscape(rawURL) + "&s=XYZ"

	psc, err := parseSignatureCipher(sc)
	if err != nil {
		t.Fatalf("parseSignatureCipher: %v", err)
	}
	if psc.sigParam != "sig" {
		t.Errorf("sigParam: got %q, want default %q", psc.sigParam, "sig")
	}
}

func TestParseSignatureCipher_MissingURL(t *testing.T) {
	_, err := parseSignatureCipher("s=CIPHERSIG&sp=sig")
	if err == nil {
		t.Fatal("expected error when 'url' field is absent")
	}
}

func TestParseSignatureCipher_MissingS(t *testing.T) {
	_, err := parseSignatureCipher("url=" + url.QueryEscape("https://example.com") + "&sp=sig")
	if err == nil {
		t.Fatal("expected error when 's' field is absent")
	}
}

// TestGetURL_SigCipherFormat verifies that GetURL can handle a format whose
// URL is delivered via a signatureCipher field (as the tv client produces).
// The mock player endpoint returns a response where the only adaptive format
// uses signatureCipher.  The stub EJS script reverses the cipher value; the
// test checks that the resolved URL contains the expected reversed sig.
func TestGetURL_SigCipherFormat(t *testing.T) {
	const (
		rawStreamURL  = "https://rr1.example.com/videoplayback?expire=99999&itag=137"
		cipherValue   = "CIPHERSIG123"
		sigParamName  = "sig"
	)

	sc := "url=" + url.QueryEscape(rawStreamURL) + "&s=" + cipherValue + "&sp=" + sigParamName

	playerResp := map[string]interface{}{
		"playabilityStatus": map[string]interface{}{"status": "OK"},
		"streamingData": map[string]interface{}{
			"adaptiveFormats": []map[string]interface{}{
				{
					"itag":            137,
					"signatureCipher": sc,
					"mimeType":        "video/mp4; codecs=\"avc1.640028\"",
					"bitrate":         3_000_000,
					"width":           1920,
					"height":          1080,
					"qualityLabel":    "1080p",
				},
			},
		},
	}
	playerRespJSON, _ := json.Marshal(playerResp)

	mux := http.NewServeMux()

	mux.HandleFunc("/youtubei/v1/player", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(playerRespJSON)
	})
	mux.HandleFunc("/watch", func(w http.ResponseWriter, r *http.Request) {
		page := `<html><body><script>var x = "/s/player/abc12345/player_ias.vflset/en_US/base.js"</script></body></html>`
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(page))
	})
	mux.HandleFunc("/s/player/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		_, _ = w.Write([]byte("// fake player JS"))
	})

	dl := buildTestDownloader(t, mux)

	got, err := dl.GetURL("testVideoID", MediaTypeVideo)
	if err != nil {
		t.Fatalf("GetURL: %v", err)
	}

	// The stub EJS reverses the cipher: "CIPHERSIG123" → "321GISREHPIC".
	// Expect ?sig=321GISREHPIC in the resolved URL.
	want := "321GISREHPIC"
	if !strings.Contains(got, sigParamName+"="+want) {
		t.Errorf("expected %s=%s in resolved URL, got %q", sigParamName, want, got)
	}
}

// TestGetURL_TVClientFallback verifies that when the tv client is rejected
// (non-OK playability status) GetURL falls back to android_vr which succeeds.
func TestGetURL_TVClientFallback(t *testing.T) {
	callCount := 0

	// OK response with a direct URL (android_vr / ios style).
	okResp := map[string]interface{}{
		"playabilityStatus": map[string]interface{}{"status": "OK"},
		"streamingData": map[string]interface{}{
			"adaptiveFormats": []map[string]interface{}{
				{
					"itag":     137,
					"url":      "https://rr1.example.com/videoplayback?expire=99999&itag=137",
					"mimeType": "video/mp4; codecs=\"avc1.640028\"",
					"bitrate":  3_000_000,
					"height":   1080,
				},
			},
		},
	}
	okRespJSON, _ := json.Marshal(okResp)

	mux := http.NewServeMux()

	mux.HandleFunc("/youtubei/v1/player", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		callCount++
		if callCount == 1 {
			// First call (tv client) – simulate bot-check rejection.
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"playabilityStatus": map[string]interface{}{
					"status": "ERROR",
					"reason": "Sign in to confirm you're not a bot",
				},
			})
			return
		}
		// Subsequent calls (android_vr, ios) – succeed.
		_, _ = w.Write(okRespJSON)
	})

	dl := buildTestDownloader(t, mux)

	got, err := dl.GetURL("botVideoID", MediaTypeVideo)
	if err != nil {
		t.Fatalf("GetURL: %v (callCount=%d)", err, callCount)
	}
	if !strings.Contains(got, "itag=137") {
		t.Errorf("expected itag=137 in URL, got %q", got)
	}
	if callCount < 2 {
		t.Errorf("expected at least 2 /player calls (tv + fallback), got %d", callCount)
	}
}
