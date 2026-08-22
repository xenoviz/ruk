package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	ReleasesURL             = "https://api.github.com/repos/xenoviz/ruk/releases?per_page=10"
	releasesPerPage         = 10
	maxReleasePages         = 100
	defaultUserAgent        = "ruk-go"
	defaultDiscoveryTimeout = 30 * time.Second
	defaultDownloadTimeout  = 5 * time.Minute
)

func (updater *Updater) discovery() Discovery {
	if updater.discover != nil {
		return updater.discover
	}
	return updater.discoverHTTP
}

func (updater *Updater) doHTTP(ctx context.Context, request *http.Request, timeout time.Duration) (*http.Response, error) {
	if request == nil {
		return nil, errors.New("update HTTP request is unavailable")
	}
	client := updater.httpClient
	if client == nil {
		// Rely on http.Client.Timeout, which bounds the whole exchange
		// including body reads. Wrapping the request context with a
		// deferred cancel here would cancel the body stream as soon as
		// this function returns, before callers finish reading it.
		client = defaultHTTPClient(timeout)
	}
	return client.Do(request)
}

func defaultHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout}
}

func (updater *Updater) discoverHTTP(ctx context.Context) ([]Release, error) {
	var remote []struct {
		Tag        string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
		Assets     []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	result := make([]Release, 0)
	nextURL := ReleasesURL
	for page := 1; page <= maxReleasePages; page++ {
		pageURL := nextURL
		var err error
		if page == 1 {
			pageURL, err = releasePageURL(ReleasesURL, page)
			if err != nil {
				return nil, err
			}
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
		if err != nil {
			return nil, err
		}
		applyGitHubAPIHeaders(request)
		response, err := updater.doHTTP(ctx, request, defaultDiscoveryTimeout)
		if err != nil {
			return nil, err
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			response.Body.Close()
			return nil, fmt.Errorf("update request failed (%s)", response.Status)
		}
		linkNext, err := nextReleasePageURL(response.Header.Get("Link"))
		if err != nil {
			response.Body.Close()
			return nil, err
		}
		remote = remote[:0]
		decodeErr := json.NewDecoder(io.LimitReader(response.Body, 4*1024*1024)).Decode(&remote)
		closeErr := response.Body.Close()
		if decodeErr != nil || closeErr != nil {
			return nil, errors.New("GitHub returned an invalid release list")
		}
		for _, item := range remote {
			version, err := ParseVersion(item.Tag)
			if err != nil || item.Draft {
				continue
			}
			assets := make(map[string]Asset, len(item.Assets))
			for _, asset := range item.Assets {
				assets[asset.Name] = Asset{Name: asset.Name, URL: asset.URL}
			}
			manifestAsset, ok := assets["ruk-release.json"]
			if !ok {
				continue
			}
			if err := validateAssetURL(manifestAsset, version.String()); err != nil {
				continue
			}
			manifestBytes, err := updater.downloadFunc()(ctx, manifestAsset)
			if err != nil {
				continue
			}
			var manifest Manifest
			if json.Unmarshal(manifestBytes, &manifest) != nil || ValidateManifest(manifest, version.String()) != nil {
				continue
			}
			validAssets := true
			for name, metadata := range manifest.Assets {
				asset, ok := assets[name]
				if !ok {
					validAssets = false
					break
				}
				asset.SHA256, asset.Size = metadata.SHA256, metadata.Size
				assets[name] = asset
			}
			if !validAssets {
				continue
			}
			result = append(result, Release{Version: version.String(), Tag: item.Tag, Prerelease: item.Prerelease, Assets: assets, Manifest: &manifest})
		}
		if linkNext != "" {
			nextURL = linkNext
			continue
		}
		if len(remote) < releasesPerPage {
			return result, nil
		}
		nextURL, err = releasePageURL(ReleasesURL, page+1)
		if err != nil {
			return nil, err
		}
	}
	return nil, errors.New("GitHub returned too many release pages")
}

func releasePageURL(base string, page int) (string, error) {
	parsed, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	query := parsed.Query()
	query.Set("page", strconv.Itoa(page))
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

// githubAuthorizationToken optionally authenticates release discovery against
// api.github.com. Shared CI runners often exhaust the unauthenticated quota and
// return 403; GH_TOKEN/GITHUB_TOKEN raise that limit without changing the public
// trust model. Asset downloads stay unauthenticated for public release files.
func githubAuthorizationToken() string {
	if token := strings.TrimSpace(os.Getenv("GH_TOKEN")); token != "" {
		return token
	}
	return strings.TrimSpace(os.Getenv("GITHUB_TOKEN"))
}

func applyGitHubAPIHeaders(request *http.Request) {
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", defaultUserAgent)
	if token := githubAuthorizationToken(); token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
}

func nextReleasePageURL(link string) (string, error) {
	for _, value := range strings.Split(link, ",") {
		parts := strings.Split(value, ";")
		if len(parts) < 2 {
			continue
		}
		relNext := false
		for _, parameter := range parts[1:] {
			if strings.TrimSpace(parameter) == `rel="next"` {
				relNext = true
				break
			}
		}
		if !relNext {
			continue
		}
		rawURL := strings.TrimSpace(parts[0])
		if len(rawURL) < 2 || rawURL[0] != '<' || rawURL[len(rawURL)-1] != '>' {
			return "", errors.New("GitHub returned an invalid release pagination link")
		}
		parsed, err := url.Parse(rawURL[1 : len(rawURL)-1])
		if err != nil || parsed.Scheme != "https" || parsed.Host != "api.github.com" || parsed.Path != "/repos/xenoviz/ruk/releases" {
			return "", errors.New("GitHub returned an untrusted release pagination link")
		}
		return parsed.String(), nil
	}
	return "", nil
}

func (updater *Updater) downloadFunc() Download {
	if updater.download != nil {
		return updater.download
	}
	return func(ctx context.Context, asset Asset) ([]byte, error) {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.URL, nil)
		if err != nil {
			return nil, err
		}
		request.Header.Set("Accept", "application/octet-stream")
		request.Header.Set("User-Agent", defaultUserAgent)
		response, err := updater.doHTTP(ctx, request, defaultDownloadTimeout)
		if err != nil {
			return nil, err
		}
		defer response.Body.Close()
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return nil, fmt.Errorf("update request failed (%s)", response.Status)
		}
		if response.ContentLength > MaxBinaryBytes {
			return nil, fmt.Errorf("%s exceeds the update size limit", asset.Name)
		}
		bytes, err := io.ReadAll(io.LimitReader(response.Body, MaxBinaryBytes+1))
		if err != nil {
			return nil, err
		}
		if len(bytes) == 0 || int64(len(bytes)) > MaxBinaryBytes {
			return nil, fmt.Errorf("%s has an invalid download size", asset.Name)
		}
		return bytes, nil
	}
}
