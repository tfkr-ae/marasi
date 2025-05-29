package marasi

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// GitHubAsset represents an asset attached to a GitHub release.
type GitHubAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
	ContentType        string `json:"content_type"`
}

// GitHubRelease represents a GitHub release.
type GitHubRelease struct {
	TagName     string        `json:"tag_name"`
	Name        string        `json:"name"`
	PublishedAt time.Time     `json:"published_at"`
	URL         string        `json:"html_url"`
	Assets      []GitHubAsset `json:"assets"` // Assets attached to the release
}

type ExtensionConfig struct {
	Name        string `yaml:"name"`
	Author      string `yaml:"author"`
	SourceURL   string `yaml:"source_url"`
	Description string `yaml:"description"`
}

func getAsset(assets []GitHubAsset, name string) (GitHubAsset, error) {
	for _, asset := range assets {
		if name == asset.Name {
			return asset, nil
		}
	}
	return GitHubAsset{}, fmt.Errorf("finding asset with name %s", name)
}
func Get(url string) (string, error) {
	res, err := http.Get(url)
	if err != nil {
		return "", fmt.Errorf("getting %s : %w", url, err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return "", fmt.Errorf("reading resp body : %w", err)
	}
	return string(body), nil
}

// ExtractAuthorRepo extracts the author/repo format from a GitHub URL.
func ExtractAuthorRepo(githubURL string) (string, error) {
	parsedURL, err := url.Parse(githubURL)
	if err != nil {
		return "", err
	}

	// Ensure the host is GitHub
	if parsedURL.Host != "github.com" {
		return "", fmt.Errorf("not a valid GitHub URL")
	}

	// Split the path and extract the author/repo part
	parts := strings.Split(strings.Trim(parsedURL.Path, "/"), "/")
	if len(parts) < 2 {
		return "", fmt.Errorf("URL path is not in the expected format")
	}

	authorRepo := fmt.Sprintf("%s/%s", parts[0], parts[1])
	return authorRepo, nil
}
func GetConfig(url string) (cfg ExtensionConfig, err error) {
	res, err := http.Get(url)
	if err != nil {
		return cfg, fmt.Errorf("getting %s : %w", url, err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return cfg, fmt.Errorf("reading resp body : %w", err)
	}
	err = yaml.Unmarshal(body, &cfg)
	if err != nil {
		return cfg, fmt.Errorf("unmarshalling yaml : %w", err)
	}
	return cfg, nil
}
func GetLatestRelease(repo string) (release GitHubRelease, config ExtensionConfig, err error) {
	extensionURL, err := ExtractAuthorRepo(repo)
	if err != nil {
		return release, config, fmt.Errorf("parsing author/repo from url %s : %w", repo, err)
	}
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases", extensionURL)
	log.Print(url)
	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return release, config, fmt.Errorf("creating request : %w", err)
	}

	// Optionally, set headers if needed (e.g., for authentication)
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	// req.Header.Set("Authorization", "token YOUR_GITHUB_TOKEN")

	resp, err := client.Do(req)
	if err != nil {
		return release, config, fmt.Errorf("getting release for %s : %w", repo, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return release, config, fmt.Errorf("github api failed with status %s : %w", resp.Status, err)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return release, config, fmt.Errorf("reading body : %w", err)
	}

	var releases []GitHubRelease
	if err := json.Unmarshal(body, &releases); err != nil {
		return release, config, fmt.Errorf("unmarshalling release: %w", err)
	}

	release = releases[0]
	fmt.Printf("Tag: %s, Name: %s, Published At: %s, URL: %s\n", release.TagName, release.Name, release.PublishedAt, release.URL)
	cfg, err := getAsset(release.Assets, "config.yaml")
	if err != nil {
		return release, config, fmt.Errorf("no config found for release : %w", err)
	}
	config, err = GetConfig(cfg.BrowserDownloadURL)
	if err != nil {
		return release, config, fmt.Errorf("error fetching config from url %s : %w", cfg.BrowserDownloadURL, err)
	}
	return release, config, nil
}
