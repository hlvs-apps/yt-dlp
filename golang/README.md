# ytdl – Go YouTube Stream URL Extractor

`ytdl` is a Go library that extracts the highest-quality stream URL from a
YouTube video.  It is intentionally minimal: it returns a URL; downloading
the content is left entirely to the caller.

## Features

* Selects the **highest-quality video** or **highest-quality audio** adaptive
  stream.
* Uses the **`tv` (TVHTML5) InnerTube client** as the primary client.  This
  client is the least likely to be bot-checked by YouTube.  Its stream URLs
  are delivered as a `signatureCipher` which the library automatically decodes
  using the yt.solver.core.js sig-challenge solver.
* Falls back to `android_vr` and then `ios` clients automatically if the `tv`
  client is unavailable for a particular video (both return direct URLs without
  signature cipher).
* Solves the **n-throttle challenge** and **sig-cipher challenge** using the
  **vendored `yt.solver.core.js`** — the same file used by the Python yt-dlp
  implementation (`yt_dlp/extractor/youtube/jsc/_builtin/vendor/yt.solver.core.js`).
* The `yt.solver.core.js` script and the `meriyah` / `astring` JavaScript
  libraries it depends on are all **embedded in the binary** via `go:embed`.
  No Node.js runtime or external script file is required.
* Safe for concurrent use from multiple goroutines.

## Requirements

| Tool   | Version  |
|--------|----------|
| Go     | ≥ 1.21   |

## Installation

```sh
go get github.com/hlvs-apps/yt-dlp/golang/ytdl
```

## Quick start

```go
package main

import (
    "fmt"
    "log"

    "github.com/hlvs-apps/yt-dlp/golang/ytdl"
)

func main() {
    // Initialize once; reuse the Downloader for all subsequent calls.
    dl, err := ytdl.NewDownloader()
    if err != nil {
        log.Fatal(err)
    }

    videoURL, err := dl.GetURL("dQw4w9WgXcQ", ytdl.MediaTypeVideo)
    if err != nil {
        log.Fatal(err)
    }
    fmt.Println("Video URL:", videoURL)

    audioURL, err := dl.GetURL("dQw4w9WgXcQ", ytdl.MediaTypeAudio)
    if err != nil {
        log.Fatal(err)
    }
    fmt.Println("Audio URL:", audioURL)
}
```

## API

### `NewDownloader() (*Downloader, error)`

Initialises the goja JavaScript runtime with the embedded `yt.solver.core.js`,
`meriyah`, and `astring` libraries and returns a ready-to-use `Downloader`.
No external files or paths are required.

### `NewDownloaderWithOptions(opts Options) (*Downloader, error)`

Like `NewDownloader` but accepts an [`Options`](#options) struct.

### `(*Downloader).GetURL(videoID string, mediaType MediaType) (string, error)`

Returns the URL of the highest-quality stream of the requested type.

* `videoID` – the 11-character YouTube video ID (e.g. `"dQw4w9WgXcQ"`).
* `mediaType` – `ytdl.MediaTypeVideo` or `ytdl.MediaTypeAudio`.

When the stream URL requires signature-cipher decoding (tv client) or contains
an n-throttle parameter, `GetURL` automatically fetches the YouTube player
JavaScript and uses the embedded `yt.solver.core.js` to resolve both,
returning a fully playable URL.

`GetURL` tries multiple InnerTube clients in order (`tv` → `android_vr` →
`ios`) and returns the first successful result.

### `Options`

```go
type Options struct {
    // HTTPClient is used for all outbound network requests.
    // Defaults to http.DefaultClient if nil.
    HTTPClient *http.Client
}
```

### `MediaType`

```go
const (
    MediaTypeVideo MediaType = "video"  // highest-quality video-only adaptive stream
    MediaTypeAudio MediaType = "audio"  // highest-quality audio-only adaptive stream
)
```

## Caching

`Downloader` automatically caches:

* The downloaded player JavaScript (keyed by player URL) so it is fetched at
  most once per player version per `Downloader` lifetime.
* The preprocessed player AST returned by the EJS script so the expensive
  JavaScript parse step runs at most once per player version.

## Updating the bundled solver

When YouTube changes its player obfuscation the `yt.solver.core.js` file in
`yt_dlp/extractor/youtube/jsc/_builtin/vendor/` will be updated as part of the
normal yt-dlp release cycle.  The Go binary simply needs to be **recompiled**
to pick up the new version — no runtime file management is required.

## Notes

* This library performs outbound HTTPS requests to `www.youtube.com`.  Ensure
  your environment allows these connections.
* The `tv` (TVHTML5) InnerTube client is used by default.  It avoids
  bot-check rejections that can occur with the `android_vr` client.
  Signature-cipher decoding is handled transparently via the embedded solver.
* `android_vr` and `ios` clients (which return direct URLs) are used as
  automatic fallbacks if the `tv` client is unavailable for a video.
* Age-restricted or otherwise unavailable videos will return an error.
