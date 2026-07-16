package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	coreosstream "github.com/coreos/stream-metadata-go/stream"
)

// installerRawURLTemplate points at openshift/installer's pinned RHCOS stream metadata — the
// same file the installer itself uses to select RHCOS for a release — fetched live over HTTPS
// rather than via a full installer checkout.
const installerRawURLTemplate = "https://raw.githubusercontent.com/openshift/installer/%s/data/data/coreos/coreos-rhel-10.json"

// marketplaceArchToStreamArch maps a Marketplace product's arch label to the stream metadata's
// architecture key.
var marketplaceArchToStreamArch = map[string]string{
	"x86_64": "x86_64",
	"arm64":  "aarch64",
}

// FetchInstallerCeiling fetches openshift/installer's pinned RHCOS stream metadata for branch and
// returns the "release" field's major.minor token (e.g. "10.2") for marketplaceArch, which is the
// ceiling of the acceptable skew band — a Marketplace AMI newer than this indicates Marketplace is
// serving the wrong image for this branch.
func FetchInstallerCeiling(ctx context.Context, branch, marketplaceArch string) (token, fullRelease string, err error) {
	streamArch, ok := marketplaceArchToStreamArch[marketplaceArch]
	if !ok {
		return "", "", fmt.Errorf("unknown marketplace arch %q", marketplaceArch)
	}

	url := fmt.Sprintf(installerRawURLTemplate, branch)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", "", err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("failed to fetch %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", "", fmt.Errorf("fetching %s returned HTTP %d: %s", url, resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", fmt.Errorf("failed to read response body from %s: %w", url, err)
	}

	token, fullRelease, err = parseStreamCeiling(body, streamArch)
	if err != nil {
		return "", "", fmt.Errorf("branch %s: %w", branch, err)
	}
	return token, fullRelease, nil
}

// parseStreamCeiling extracts the aws artifact's release token from raw stream metadata JSON for
// streamArch, split out from FetchInstallerCeiling so it can be tested against a fixture without a
// live network call.
func parseStreamCeiling(body []byte, streamArch string) (token, fullRelease string, err error) {
	var streamData coreosstream.Stream
	if err := json.Unmarshal(body, &streamData); err != nil {
		return "", "", fmt.Errorf("failed to parse stream metadata: %w", err)
	}

	arch, err := streamData.GetArchitecture(streamArch)
	if err != nil {
		return "", "", err
	}

	awsArtifact, ok := arch.Artifacts["aws"]
	if !ok {
		return "", "", fmt.Errorf("stream metadata has no aws artifact for %s", streamArch)
	}

	fullRelease = awsArtifact.Release
	token, err = releaseToken(fullRelease)
	if err != nil {
		return "", "", err
	}
	return token, fullRelease, nil
}

// releaseToken derives the major.minor token from a full RHCOS release string, e.g.
// "10.2.20260423-0" -> "10.2".
func releaseToken(release string) (string, error) {
	parts := strings.SplitN(release, ".", 3)
	if len(parts) < 2 {
		return "", fmt.Errorf("unexpected RHCOS release string format: %q", release)
	}
	return parts[0] + "." + parts[1], nil
}
