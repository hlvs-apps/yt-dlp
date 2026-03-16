// Integration tests for the yt.solver.core.js EJS challenge solver.
//
// These tests mirror test_ejs_integration.py from the Python yt-dlp test suite.
// They download real YouTube player JavaScript files and verify that the
// embedded solver produces the expected n-throttle and sig-cipher outputs.
//
// The tests are skipped when the -short flag is set, matching the behaviour
// of the @pytest.mark.download marker used in the Python tests.

package ytdl

import (
	"fmt"
	"io"
	"net/http"
	"testing"
)

// challengeType mirrors JsChallengeType in the Python provider.
type challengeType string

const (
	challengeN   challengeType = "n"
	challengeSig challengeType = "sig"
)

// challengeCase holds a single challenge test vector.
// Each case corresponds to one Challenge() entry in test_ejs_integration.py.
type challengeCase struct {
	// playerHash is the short hash identifying the YouTube player version,
	// e.g. "edc3ba07".
	playerHash string
	// variantURL is the path suffix appended to
	// https://www.youtube.com/s/player/<hash>/ to obtain the player JS URL.
	variantURL string
	// challengeKind is either challengeN or challengeSig.
	challengeKind challengeType
	// values maps each input challenge string to the expected solved value.
	values map[string]string
}

// playerURL returns the full URL used to fetch the player JavaScript.
func (c challengeCase) playerURL() string {
	return fmt.Sprintf("https://www.youtube.com/s/player/%s/%s", c.playerHash, c.variantURL)
}

// tvVariant is the path suffix for the TVHTML5 player variant used in the
// Python tests (Variant.tv).
const tvVariant = "tv-player-ias.vflset/tv-player-ias.js"

// integrationChallenges contains the same test vectors as CHALLENGES in
// yt_dlp/test/test_jsc/test_ejs_integration.py.
var integrationChallenges = []challengeCase{
	// player 20518
	{
		playerHash:    "edc3ba07",
		variantURL:    tvVariant,
		challengeKind: challengeN,
		values: map[string]string{
			"BQoJvGBkC2nj1ZZLK-": "-m-se9fQVnvEofLx",
		},
	},
	{
		playerHash:    "edc3ba07",
		variantURL:    tvVariant,
		challengeKind: challengeSig,
		values: map[string]string{
			"NJAJEij0EwRgIhAI0KExTgjfPk-MPM9MAdzyyPRt=BM8-XO5tm5hlMCSVpAiEAv7eP3CURqZNSPow8BXXAoazVoXgeMP7gH9BdylHCwgw=gwzz": "zwg=wgwCHlydB9zg7PMegXoVzaoAXXB8woPSNZqRUC3Pe7vAEiApVSCMlh5mt5OX-8MB=tRPyyEdAM9MPM-kPfjgTxEK0IAhIgRwE0jiz",
		},
	},
	// player 20521
	{
		playerHash:    "316b61b4",
		variantURL:    tvVariant,
		challengeKind: challengeN,
		values: map[string]string{
			"IlLiA21ny7gqA2m4p37": "GchRcsUC_WmnhOUVGV",
		},
	},
	{
		playerHash:    "316b61b4",
		variantURL:    tvVariant,
		challengeKind: challengeSig,
		values: map[string]string{
			"NJAJEij0EwRgIhAI0KExTgjfPk-MPM9MAdzyyPRt=BM8-XO5tm5hlMCSVpAiEAv7eP3CURqZNSPow8BXXAoazVoXgeMP7gH9BdylHCwgw=gwzz": "tJAJEij0EwRgIhAI0KExTgjfPk-MPM9MAdzyyPRN=BM8-XO5tm5hlMCSVpAiEAv7eP3CURqZNSPow8BXXAoazVoXgeMP7gH9BdylHCwgw=gwz",
		},
	},
	// player 20522
	{
		playerHash:    "74edf1a3",
		variantURL:    tvVariant,
		challengeKind: challengeN,
		values: map[string]string{
			"IlLiA21ny7gqA2m4p37":    "9nRTxrbM1f0yHg",
			"eabGFpsUKuWHXGh6FR4":    "izmYqDEY6kl7Sg",
			"eabGF/ps%UK=uWHXGh6FR4": "LACmqlhaBpiPlgE-a",
		},
	},
	{
		playerHash:    "74edf1a3",
		variantURL:    tvVariant,
		challengeKind: challengeSig,
		values: map[string]string{
			"NJAJEij0EwRgIhAI0KExTgjfPk-MPM9MAdzyyPRt=BM8-XO5tm5hlMCSVpAiEAv7eP3CURqZNSPow8BXXAoazVoXgeMP7gH9BdylHCwgw=gwzz": "NJAJEij0EwRgIhAI0KExTgjfPk-MPM9MAdzyyPRt=BM8-XO5tm5hzMCSVpAiEAv7eP3CURqZNSPow8BXXAoazVoXgeMP7gH9BdylHCwgw=gwzl",
		},
	},
	// player 20523
	{
		playerHash:    "901741ab",
		variantURL:    tvVariant,
		challengeKind: challengeN,
		values: map[string]string{
			"BQoJvGBkC2nj1ZZLK-": "UMPovvBZRh-sjb",
		},
	},
	{
		playerHash:    "901741ab",
		variantURL:    tvVariant,
		challengeKind: challengeSig,
		values: map[string]string{
			"NJAJEij0EwRgIhAI0KExTgjfPk-MPM9MAdzyyPRt=BM8-XO5tm5hlMCSVpAiEAv7eP3CURqZNSPow8BXXAoazVoXgeMP7gH9BdylHCwgw=gwzz": "wgwCHlydB9Hg7PMegXoVzaoAXXB8woPSNZqRUC3Pe7vAEiApVSCMlhwmt5ON-8MB=5RPyyzdAM9MPM-kPfjgTxEK0IAhIgRwE0jiEJA",
		},
	},
	// player 20524
	{
		playerHash:    "e7573094",
		variantURL:    tvVariant,
		challengeKind: challengeN,
		values: map[string]string{
			"IlLiA21ny7gqA2m4p37": "3KuQ3235dojTSjo4",
		},
	},
	{
		playerHash:    "e7573094",
		variantURL:    tvVariant,
		challengeKind: challengeSig,
		values: map[string]string{
			"NJAJEij0EwRgIhAI0KExTgjfPk-MPM9MAdzyyPRt=BM8-XO5tm5hlMCSVpAiEAv7eP3CURqZNSPow8BXXAoazVoXgeMP7gH9BdylHCwgw=gwzz": "yEij0EwRgIhAI0KExTgjfPk-MPM9MAdzyNPRt=BM8-XO5tm5hlMCSVNAiEAvpeP3CURqZJSPow8BXXAoazVoXgeMP7gH9BdylHCwgw=g",
		},
	},
}

// fetchPlayerJS downloads the player JavaScript at the given URL.
func fetchPlayerJSFromURL(t *testing.T, playerURL string) string {
	t.Helper()
	resp, err := http.Get(playerURL) //nolint:noctx // integration test
	if err != nil {
		t.Fatalf("fetch player JS %s: %v", playerURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("fetch player JS %s: HTTP %d", playerURL, resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read player JS %s: %v", playerURL, err)
	}
	return string(data)
}

// TestEJSChallenges mirrors test_bulk_requests from test_ejs_integration.py.
// Each sub-test downloads the real player JS and checks that the embedded
// yt.solver.core.js produces the exact expected output for every challenge value.
//
// The test is skipped when -short is set (same as @pytest.mark.download).
func TestEJSChallenges(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping download test in -short mode")
	}

	dl, err := NewDownloader()
	if err != nil {
		t.Fatalf("NewDownloader: %v", err)
	}

	// playerJSCache avoids re-downloading the same player JS file for multiple
	// challenge cases that share the same player hash.
	playerJSCache := make(map[string]string)

	for _, tc := range integrationChallenges {
		tc := tc
		name := fmt.Sprintf("%s/%s", tc.playerHash, tc.challengeKind)
		t.Run(name, func(t *testing.T) {
			pURL := tc.playerURL()
			if _, ok := playerJSCache[pURL]; !ok {
				playerJSCache[pURL] = fetchPlayerJSFromURL(t, pURL)
			}
			playerJS := playerJSCache[pURL]

			inputs := make([]string, 0, len(tc.values))
			for in := range tc.values {
				inputs = append(inputs, in)
			}

			results, err := dl.runEJS(pURL, playerJS, string(tc.challengeKind), inputs)
			if err != nil {
				t.Fatalf("runEJS: %v", err)
			}

			for in, want := range tc.values {
				got, ok := results[in]
				if !ok {
					t.Errorf("no result for input %q; got map %v", in, results)
					continue
				}
				if got != want {
					t.Errorf("input %q: got %q, want %q", in, got, want)
				}
			}
		})
	}
}
