// Package ytdl provides YouTube stream URL extraction using the yt-dlp EJS
// challenge solver script and the goja JavaScript engine.
//
// Usage:
//
//	dl, err := ytdl.NewDownloader("/path/to/yt.solver.core.js")
//	if err != nil {
//	    log.Fatal(err)
//	}
//	videoURL, err := dl.GetURL("dQw4w9WgXcQ", ytdl.MediaTypeVideo)
//	audioURL, err := dl.GetURL("dQw4w9WgXcQ", ytdl.MediaTypeAudio)
package ytdl

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/dop251/goja"

	_ "embed"
)

// meriyahJS and astringJS are vendored UMD bundles embedded into the binary so
// the EJS core script's dependency on the meriyah and astring JavaScript
// libraries is satisfied without a Node.js runtime.
//
//go:embed js/meriyah.umd.min.js
var meriyahJS []byte

//go:embed js/astring.min.js
var astringJS []byte

// MediaType selects the kind of stream URL to return.
type MediaType string

const (
	// MediaTypeVideo returns the highest-quality video-only adaptive stream.
	MediaTypeVideo MediaType = "video"
	// MediaTypeAudio returns the highest-quality audio-only adaptive stream.
	MediaTypeAudio MediaType = "audio"
)

// Options carries optional configuration for [NewDownloaderWithOptions].
type Options struct {
	// HTTPClient is used for all outbound network requests.  If nil the
	// default http.Client is used.
	HTTPClient *http.Client
}

// Downloader extracts YouTube stream URLs.
//
// A single Downloader instance is safe to use from multiple goroutines; an
// internal mutex serialises access to the embedded goja JavaScript runtime.
type Downloader struct {
	vm      *goja.Runtime
	jscFunc goja.Callable
	jsMu    sync.Mutex

	httpClient *http.Client

	// playerJSCache maps a player URL to the downloaded player JavaScript
	// source, avoiding repeated HTTP fetches for the same player version.
	playerJSCache sync.Map // map[string]string

	// preprocessedCache maps a player URL to the JSON-serialisable
	// preprocessed_player object returned by the EJS jsc() function after
	// the first call.  This lets subsequent calls skip the expensive AST
	// parse step inside the EJS script.
	preprocessedCache sync.Map // map[string]json.RawMessage
}

const innertubePlayerEndpoint = "https://www.youtube.com/youtubei/v1/player"

// innertubeClientConfig groups all parameters needed to make an InnerTube
// /player request for a particular client identity.
type innertubeClientConfig struct {
	// context is the JSON object placed under the "client" key inside
	// "context" in the request body.
	context map[string]interface{}
	// clientNameID is the numeric identifier sent in X-Youtube-Client-Name.
	clientNameID string
	// clientVersion is sent in X-Youtube-Client-Version.
	clientVersion string
	// userAgent is sent in the User-Agent header.
	userAgent string
	// requiresJSPlayer indicates whether stream URLs from this client are
	// wrapped in a signatureCipher field that must be deciphered using the
	// YouTube player JavaScript.  When false, URLs are direct.
	requiresJSPlayer bool
}

// clientTV is the TVHTML5 InnerTube client (client ID 7).
//
// This is the primary client.  It is the least likely to be bot-checked by
// YouTube because it resembles a legitimate Smart TV app.  Its stream URLs
// come wrapped in a signatureCipher field that is decoded on the fly by the
// EJS sig-challenge solver.
var clientTV = innertubeClientConfig{
	context: map[string]interface{}{
		"clientName":    "TVHTML5",
		"clientVersion": "7.20260114.12.00",
		"userAgent":     "Mozilla/5.0 (ChromiumStylePlatform) Cobalt/25.lts.30.1034943-gold (unlike Gecko), Unknown_TV_Unknown_0/Unknown (Unknown, Unknown)",
	},
	clientNameID:     "7",
	clientVersion:    "7.20260114.12.00",
	userAgent:        "Mozilla/5.0 (ChromiumStylePlatform) Cobalt/25.lts.30.1034943-gold (unlike Gecko), Unknown_TV_Unknown_0/Unknown (Unknown, Unknown)",
	requiresJSPlayer: true,
}

// clientAndroidVR is the ANDROID_VR InnerTube client (client ID 28).
//
// Returns direct stream URLs (no signature cipher).  Used as a fallback when
// the tv client is unavailable.
var clientAndroidVR = innertubeClientConfig{
	context: map[string]interface{}{
		"clientName":        "ANDROID_VR",
		"clientVersion":     "1.65.10",
		"deviceMake":        "Oculus",
		"deviceModel":       "Quest 3",
		"androidSdkVersion": 32,
		"userAgent":         "com.google.android.apps.youtube.vr.oculus/1.65.10 (Linux; U; Android 12L; eureka-user Build/SQ3A.220605.009.A1) gzip",
		"osName":            "Android",
		"osVersion":         "12L",
	},
	clientNameID:  "28",
	clientVersion: "1.65.10",
	userAgent:     "com.google.android.apps.youtube.vr.oculus/1.65.10 (Linux; U; Android 12L; eureka-user Build/SQ3A.220605.009.A1) gzip",
}

// clientIOS is the IOS InnerTube client (client ID 5).
//
// Returns direct stream URLs (no signature cipher).  Used as a final fallback.
var clientIOS = innertubeClientConfig{
	context: map[string]interface{}{
		"clientName":    "IOS",
		"clientVersion": "21.02.3",
		"deviceMake":    "Apple",
		"deviceModel":   "iPhone16,2",
		"userAgent":     "com.google.ios.youtube/21.02.3 (iPhone16,2; U; CPU iOS 18_3_2 like Mac OS X;)",
		"osName":        "iPhone",
		"osVersion":     "18.3.2.22D82",
	},
	clientNameID:  "5",
	clientVersion: "21.02.3",
	userAgent:     "com.google.ios.youtube/21.02.3 (iPhone16,2; U; CPU iOS 18_3_2 like Mac OS X;)",
}

// defaultClients is the ordered list of InnerTube clients tried by GetURL.
// tv (TVHTML5) is tried first because it avoids bot-check rejections.
// android_vr and ios are fallbacks that return direct (non-ciphered) URLs.
var defaultClients = []innertubeClientConfig{clientTV, clientAndroidVR, clientIOS}

// playerURLRe matches the player JavaScript URL embedded in the YouTube
// watch-page HTML, e.g. /s/player/HASH/player_ias.vflset/en_US/base.js
var playerURLRe = regexp.MustCompile(`/s/player/[a-zA-Z0-9_-]+/[^\s"'\\]+\.js`)

// -----------------------------------------------------------------------
// Public API
// -----------------------------------------------------------------------

// NewDownloader creates a [Downloader] that uses the EJS core script located
// at ejsScriptPath.
//
// The file should be a yt.solver.core.js file from
// https://github.com/yt-dlp/ejs/releases.  The meriyah and astring
// JavaScript dependencies are embedded in the binary and do not need to be
// provided separately.
func NewDownloader(ejsScriptPath string) (*Downloader, error) {
	return NewDownloaderWithOptions(ejsScriptPath, Options{})
}

// NewDownloaderWithOptions is like [NewDownloader] but accepts additional
// [Options].
func NewDownloaderWithOptions(ejsScriptPath string, opts Options) (*Downloader, error) {
	ejsCode, err := os.ReadFile(ejsScriptPath)
	if err != nil {
		return nil, fmt.Errorf("ytdl: read EJS script %q: %w", ejsScriptPath, err)
	}

	vm := goja.New()

	// Expose globalThis so that UMD bundles that use it can find the global
	// object even in older versions of goja.
	if err := vm.Set("globalThis", vm.GlobalObject()); err != nil {
		return nil, fmt.Errorf("ytdl: set globalThis: %w", err)
	}

	// Load the meriyah UMD bundle → sets globalThis.meriyah
	if _, err := vm.RunString(string(meriyahJS)); err != nil {
		return nil, fmt.Errorf("ytdl: load meriyah: %w", err)
	}

	// Load the astring bundle → sets globalThis.astring
	if _, err := vm.RunString(string(astringJS)); err != nil {
		return nil, fmt.Errorf("ytdl: load astring: %w", err)
	}

	// Load the EJS core script → defines globalThis.jsc using the global
	// meriyah and astring objects.
	if _, err := vm.RunString(string(ejsCode)); err != nil {
		return nil, fmt.Errorf("ytdl: load EJS script: %w", err)
	}

	jscVal := vm.Get("jsc")
	if jscVal == nil || goja.IsUndefined(jscVal) || goja.IsNull(jscVal) {
		return nil, fmt.Errorf("ytdl: EJS script did not export a 'jsc' function")
	}
	jscFunc, ok := goja.AssertFunction(jscVal)
	if !ok {
		return nil, fmt.Errorf("ytdl: 'jsc' is not a callable function")
	}

	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{}
	}

	return &Downloader{
		vm:         vm,
		jscFunc:    jscFunc,
		httpClient: client,
	}, nil
}

// GetURL returns the URL of the highest-quality stream for the given YouTube
// video ID and media type.
//
// videoID must be the 11-character YouTube video identifier
// (e.g. "dQw4w9WgXcQ"), not a full URL.
//
// mediaType must be [MediaTypeVideo] or [MediaTypeAudio].
//
// GetURL tries multiple InnerTube clients in order (android_vr first, ios
// as fallback).  If the preferred client is rejected by YouTube (e.g. with
// "Sign in to confirm you're not a bot") the next client is tried
// automatically.
//
// When the stream URL contains an n-throttle parameter, GetURL automatically
// downloads the YouTube player JavaScript and uses the EJS script to solve
// the challenge, returning a de-throttled URL.
func (d *Downloader) GetURL(videoID string, mediaType MediaType) (string, error) {
	if videoID == "" {
		return "", fmt.Errorf("ytdl: videoID must not be empty")
	}
	if mediaType != MediaTypeVideo && mediaType != MediaTypeAudio {
		return "", fmt.Errorf("ytdl: unknown MediaType %q; use MediaTypeVideo or MediaTypeAudio", mediaType)
	}

	var lastErr error
	for _, client := range defaultClients {
		streamURL, err := d.getURLWithClient(videoID, mediaType, client)
		if err == nil {
			return streamURL, nil
		}
		lastErr = err
	}
	return "", lastErr
}

// getURLWithClient is the per-client implementation called by GetURL.
func (d *Downloader) getURLWithClient(videoID string, mediaType MediaType, client innertubeClientConfig) (string, error) {
	pr, err := d.fetchPlayerResponse(videoID, client)
	if err != nil {
		return "", fmt.Errorf("ytdl: %w", err)
	}

	if status := pr.PlayabilityStatus.Status; status != "OK" {
		reason := pr.PlayabilityStatus.Reason
		if reason == "" {
			reason = status
		}
		return "", fmt.Errorf("ytdl: video %q is not playable: %s", videoID, reason)
	}

	fmts := collectFormats(pr, mediaType)
	if len(fmts) == 0 {
		return "", fmt.Errorf("ytdl: no %s formats available for video %q", mediaType, videoID)
	}

	sortFormats(fmts, mediaType)
	best := fmts[0]

	streamURL, err := d.resolveFormatURL(videoID, best)
	if err != nil {
		return "", fmt.Errorf("resolve stream URL: %w", err)
	}
	return streamURL, nil
}

// -----------------------------------------------------------------------
// InnerTube player response
// -----------------------------------------------------------------------

// playerResponse is a minimal representation of the InnerTube /player
// response.
type playerResponse struct {
	PlayabilityStatus struct {
		Status string `json:"status"`
		Reason string `json:"reason"`
	} `json:"playabilityStatus"`
	StreamingData struct {
		Formats         []streamFormat `json:"formats"`
		AdaptiveFormats []streamFormat `json:"adaptiveFormats"`
	} `json:"streamingData"`
}

// streamFormat represents a single audio or video stream descriptor inside a
// player response.
type streamFormat struct {
	Itag            int    `json:"itag"`
	URL             string `json:"url"`
	MimeType        string `json:"mimeType"`
	Bitrate         int    `json:"bitrate"`
	AverageBitrate  int    `json:"averageBitrate"`
	Width           int    `json:"width"`
	Height          int    `json:"height"`
	QualityLabel    string `json:"qualityLabel"`
	Quality         string `json:"quality"`
	AudioQuality    string `json:"audioQuality"`
	SignatureCipher string `json:"signatureCipher"`
}

// effectiveBitrate returns the best available bitrate value.
func (f streamFormat) effectiveBitrate() int {
	if f.AverageBitrate > 0 {
		return f.AverageBitrate
	}
	return f.Bitrate
}

func (d *Downloader) fetchPlayerResponse(videoID string, client innertubeClientConfig) (*playerResponse, error) {
	body := map[string]interface{}{
		"context": map[string]interface{}{
			"client": client.context,
		},
		"videoId": videoID,
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal player request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, innertubePlayerEndpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("create player request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", client.userAgent)
	req.Header.Set("X-Youtube-Client-Name", client.clientNameID)
	req.Header.Set("X-Youtube-Client-Version", client.clientVersion)

	resp, err := d.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("player API request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("player API returned HTTP %d", resp.StatusCode)
	}

	var pr playerResponse
	if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
		return nil, fmt.Errorf("decode player response: %w", err)
	}
	return &pr, nil
}

// -----------------------------------------------------------------------
// Format selection
// -----------------------------------------------------------------------

// internalFormat is a normalised representation used for sorting.
type internalFormat struct {
	URL             string
	SignatureCipher string // non-empty when the stream URL requires sig deciphering
	MimeType        string
	Bitrate         int
	Height          int
	AudioQuality    string
}

// collectFormats extracts the relevant stream formats from a player response.
//
// For MediaTypeVideo it returns adaptive video-only streams.
// For MediaTypeAudio it returns adaptive audio-only streams.
// Formats with either a direct URL or a signatureCipher are included.
// Formats with neither are silently skipped.
func collectFormats(pr *playerResponse, mediaType MediaType) []internalFormat {
	var out []internalFormat
	for _, f := range pr.StreamingData.AdaptiveFormats {
		if f.URL == "" && f.SignatureCipher == "" {
			continue // no usable URL; skip
		}
		mime := strings.ToLower(f.MimeType)
		switch mediaType {
		case MediaTypeVideo:
			if !strings.HasPrefix(mime, "video/") {
				continue
			}
		case MediaTypeAudio:
			if !strings.HasPrefix(mime, "audio/") {
				continue
			}
		}
		out = append(out, internalFormat{
			URL:             f.URL,
			SignatureCipher: f.SignatureCipher,
			MimeType:        f.MimeType,
			Bitrate:         f.effectiveBitrate(),
			Height:          f.Height,
			AudioQuality:    f.AudioQuality,
		})
	}
	return out
}

// sortFormats orders formats from best to worst quality in-place.
//
// For video the primary sort key is height (resolution), with bitrate as a
// tiebreaker.  For audio the only key is bitrate.
func sortFormats(fmts []internalFormat, mediaType MediaType) {
	sort.SliceStable(fmts, func(i, j int) bool {
		a, b := fmts[i], fmts[j]
		if mediaType == MediaTypeVideo {
			if a.Height != b.Height {
				return a.Height > b.Height
			}
		}
		return a.Bitrate > b.Bitrate
	})
}

// -----------------------------------------------------------------------
// URL challenge solving
// -----------------------------------------------------------------------

// resolveFormatURL returns the playable URL for a stream format, solving any
// signature cipher and/or n-throttle challenge as needed.
//
// For formats with a direct URL (android_vr, ios clients) only the n-challenge
// is relevant.  For formats with a signatureCipher (tv client) the signature
// is decoded first, then the n-challenge is applied to the resulting URL.
func (d *Downloader) resolveFormatURL(videoID string, f internalFormat) (string, error) {
	streamURL := f.URL

	if f.SignatureCipher != "" {
		resolved, err := d.resolveSigCipher(videoID, f.SignatureCipher)
		if err != nil {
			return "", fmt.Errorf("resolve sig cipher: %w", err)
		}
		streamURL = resolved
	}

	solved, err := d.solveNChallenge(videoID, streamURL)
	if err != nil {
		// Return the un-throttled URL rather than failing completely.
		return streamURL, nil
	}
	return solved, nil
}

// parsedSignatureCipher holds the components extracted from a stream's
// signatureCipher field.
type parsedSignatureCipher struct {
	streamURL string // base stream URL
	cipher    string // the ciphered signature value (the "s" parameter)
	sigParam  string // query parameter name for the solved sig (usually "sig")
}

// parseSignatureCipher parses the URL-encoded signatureCipher field that
// YouTube embeds in stream formats returned by clients such as TVHTML5.
//
// The field is a query string of the form:
//
//	url=<encoded-url>&s=<ciphered-sig>&sp=<param-name>
func parseSignatureCipher(sc string) (*parsedSignatureCipher, error) {
	vals, err := url.ParseQuery(sc)
	if err != nil {
		return nil, fmt.Errorf("parse signatureCipher: %w", err)
	}
	streamURL := vals.Get("url")
	if streamURL == "" {
		return nil, fmt.Errorf("signatureCipher missing 'url' field")
	}
	cipher := vals.Get("s")
	if cipher == "" {
		return nil, fmt.Errorf("signatureCipher missing 's' field")
	}
	sigParam := vals.Get("sp")
	if sigParam == "" {
		sigParam = "sig" // YouTube default
	}
	return &parsedSignatureCipher{
		streamURL: streamURL,
		cipher:    cipher,
		sigParam:  sigParam,
	}, nil
}

// resolveSigCipher decodes a signatureCipher field into a direct stream URL
// by using the EJS script to solve the sig challenge.
func (d *Downloader) resolveSigCipher(videoID, signatureCipher string) (string, error) {
	psc, err := parseSignatureCipher(signatureCipher)
	if err != nil {
		return "", err
	}

	playerURL, err := d.fetchPlayerURL(videoID)
	if err != nil {
		return "", fmt.Errorf("fetch player URL for sig: %w", err)
	}
	playerJS, err := d.fetchPlayerJS(playerURL)
	if err != nil {
		return "", fmt.Errorf("fetch player JS for sig: %w", err)
	}

	results, err := d.runEJS(playerURL, playerJS, "sig", []string{psc.cipher})
	if err != nil {
		return "", fmt.Errorf("EJS sig challenge: %w", err)
	}
	solved, ok := results[psc.cipher]
	if !ok || solved == "" {
		return "", fmt.Errorf("EJS returned no result for sig challenge")
	}

	// Append the solved signature to the stream URL.
	u, parseErr := url.Parse(psc.streamURL)
	if parseErr != nil {
		return "", fmt.Errorf("parse stream URL from signatureCipher: %w", parseErr)
	}
	q := u.Query()
	q.Set(psc.sigParam, solved)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// solveNChallenge checks whether streamURL contains a YouTube n-throttle
// parameter and, if so, uses the EJS script to transform it into an
// unthrottled value.  If no n parameter is present the original URL is
// returned unchanged.
func (d *Downloader) solveNChallenge(videoID, streamURL string) (string, error) {
	parsed, err := url.Parse(streamURL)
	if err != nil {
		return streamURL, nil
	}
	query := parsed.Query()
	nParam := query.Get("n")
	if nParam == "" {
		return streamURL, nil
	}

	playerURL, err := d.fetchPlayerURL(videoID)
	if err != nil {
		return "", fmt.Errorf("fetch player URL: %w", err)
	}

	playerJS, err := d.fetchPlayerJS(playerURL)
	if err != nil {
		return "", fmt.Errorf("fetch player JS: %w", err)
	}

	results, err := d.runEJS(playerURL, playerJS, "n", []string{nParam})
	if err != nil {
		return "", fmt.Errorf("EJS n-challenge: %w", err)
	}

	solved, ok := results[nParam]
	if !ok || solved == "" {
		return "", fmt.Errorf("EJS returned no result for n-challenge %q", nParam)
	}

	query.Set("n", solved)
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

// fetchPlayerURL retrieves the URL of the YouTube player JavaScript file for
// a given video by fetching the watch page and searching for the embedded
// player path.
func (d *Downloader) fetchPlayerURL(videoID string) (string, error) {
	watchURL := "https://www.youtube.com/watch?v=" + url.QueryEscape(videoID)
	req, err := http.NewRequest(http.MethodGet, watchURL, nil)
	if err != nil {
		return "", fmt.Errorf("create watch request: %w", err)
	}
	// Use a desktop browser UA so that YouTube returns the full page.
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	resp, err := d.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch watch page: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read watch page: %w", err)
	}

	match := playerURLRe.Find(data)
	if match == nil {
		return "", fmt.Errorf("player JS URL not found in watch page for video %q", videoID)
	}
	return "https://www.youtube.com" + string(match), nil
}

// fetchPlayerJS downloads the YouTube player JavaScript source.  Results are
// cached keyed by player URL so that the file is fetched at most once per
// player version per Downloader lifetime.
func (d *Downloader) fetchPlayerJS(playerURL string) (string, error) {
	if cached, ok := d.playerJSCache.Load(playerURL); ok {
		return cached.(string), nil
	}

	resp, err := d.httpClient.Get(playerURL)
	if err != nil {
		return "", fmt.Errorf("fetch player JS: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("player JS returned HTTP %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read player JS: %w", err)
	}

	js := string(data)
	d.playerJSCache.Store(playerURL, js)
	return js, nil
}

// ejsInput is the JSON payload passed to the jsc() function defined by the
// EJS core script.
type ejsInput struct {
	// Type is either "player" (pass the raw player JS) or "preprocessed"
	// (pass a previously computed preprocessed_player value).
	Type string `json:"type"`

	// Player is the raw YouTube player JavaScript source.  Used when
	// Type == "player".
	Player string `json:"player,omitempty"`

	// PreprocessedPlayer is the cached AST analysis from a previous call.
	// Used when Type == "preprocessed".
	PreprocessedPlayer json.RawMessage `json:"preprocessed_player,omitempty"`

	// Requests is the list of challenge requests to solve.
	Requests []ejsRequest `json:"requests"`

	// OutputPreprocessed requests that the response include the
	// preprocessed player data so it can be cached.
	OutputPreprocessed bool `json:"output_preprocessed"`
}

type ejsRequest struct {
	Type       string   `json:"type"`
	Challenges []string `json:"challenges"`
}

type ejsOutput struct {
	Type               string            `json:"type"`
	Error              string            `json:"error,omitempty"`
	Responses          []ejsResponse     `json:"responses,omitempty"`
	PreprocessedPlayer json.RawMessage   `json:"preprocessed_player,omitempty"`
}

type ejsResponse struct {
	Type  string            `json:"type"`
	Error string            `json:"error,omitempty"`
	Data  map[string]string `json:"data,omitempty"`
}

// runEJS calls the jsc() function from the EJS core script to solve one or
// more challenges of the given type ("n" or "sig").  It returns a map of
// challenge value → solved value.
//
// runEJS is safe to call concurrently; it serialises access to the goja VM
// with a mutex.
func (d *Downloader) runEJS(playerURL, playerJS, challengeType string, challenges []string) (map[string]string, error) {
	// Build the input JSON.
	var input ejsInput
	input.Requests = []ejsRequest{
		{Type: challengeType, Challenges: challenges},
	}

	if cached, ok := d.preprocessedCache.Load(playerURL); ok {
		input.Type = "preprocessed"
		input.PreprocessedPlayer = cached.(json.RawMessage)
	} else {
		input.Type = "player"
		input.Player = playerJS
		input.OutputPreprocessed = true
	}

	inputJSON, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("marshal EJS input: %w", err)
	}

	// Serialise access to the goja runtime.
	d.jsMu.Lock()
	defer d.jsMu.Unlock()

	// Parse the input JSON into a goja value and call jsc().
	inputVal, err := d.vm.RunString("(" + string(inputJSON) + ")")
	if err != nil {
		return nil, fmt.Errorf("parse EJS input in VM: %w", err)
	}

	result, err := d.jscFunc(goja.Undefined(), inputVal)
	if err != nil {
		return nil, fmt.Errorf("jsc() call failed: %w", err)
	}

	// Serialise the result back to JSON for easy Go-side handling.
	resultJSON, err := json.Marshal(result.Export())
	if err != nil {
		return nil, fmt.Errorf("marshal EJS output: %w", err)
	}

	var output ejsOutput
	if err := json.Unmarshal(resultJSON, &output); err != nil {
		return nil, fmt.Errorf("decode EJS output: %w", err)
	}

	if output.Type == "error" {
		return nil, fmt.Errorf("EJS error: %s", output.Error)
	}

	// Cache the preprocessed player for future calls with the same player.
	if len(output.PreprocessedPlayer) > 0 {
		d.preprocessedCache.Store(playerURL, output.PreprocessedPlayer)
	}

	if len(output.Responses) == 0 {
		return nil, fmt.Errorf("EJS returned no responses")
	}

	resp := output.Responses[0]
	if resp.Type == "error" {
		return nil, fmt.Errorf("EJS %s-challenge error: %s", challengeType, resp.Error)
	}

	return resp.Data, nil
}
