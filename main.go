package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

var (
	latestTag   string
	latestMu    sync.RWMutex
	githubOrg   = envOrDefault("GITHUB_ORG", "ThurayaTraceCloud")
	githubRepo  = envOrDefault("GITHUB_REPO", "agent")
	githubToken = os.Getenv("GITHUB_TOKEN")
	// Custom transport that does NOT follow redirects — we handle them manually
	noRedirectClient = &http.Client{
		Timeout: 5 * time.Minute,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	httpClient = &http.Client{Timeout: 5 * time.Minute}
)

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

type githubRelease struct {
	TagName string        `json:"tag_name"`
	Assets  []githubAsset `json:"assets"`
}

type githubAsset struct {
	Name string `json:"name"`
	URL  string `json:"url"` // API URL for downloading
}

func fetchLatestTag() (string, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", githubOrg, githubRepo)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if githubToken != "" {
		req.Header.Set("Authorization", "Bearer "+githubToken)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github api returned %d", resp.StatusCode)
	}

	var release githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return "", err
	}
	return release.TagName, nil
}

func refreshLatestTag() {
	tag, err := fetchLatestTag()
	if err != nil {
		log.Printf("failed to fetch latest tag: %v", err)
		return
	}
	latestMu.Lock()
	latestTag = tag
	latestMu.Unlock()
	log.Printf("cached latest tag: %s", tag)
}

func getLatestTag() string {
	latestMu.RLock()
	defer latestMu.RUnlock()
	return latestTag
}

// fetchAssetURL finds the API download URL for a specific asset in a release.
func fetchAssetURL(version, filename string) (string, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/tags/%s", githubOrg, githubRepo, version)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if githubToken != "" {
		req.Header.Set("Authorization", "Bearer "+githubToken)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github api returned %d for tag %s", resp.StatusCode, version)
	}

	var release githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return "", err
	}

	for _, asset := range release.Assets {
		if asset.Name == filename {
			return asset.URL, nil
		}
	}
	return "", fmt.Errorf("asset %q not found in release %s", filename, version)
}

// proxyAsset fetches the GitHub release asset via the API and streams it to the client.
func proxyAsset(w http.ResponseWriter, r *http.Request, version, filename string) {
	assetURL, err := fetchAssetURL(version, filename)
	if err != nil {
		log.Printf("error finding asset %s/%s: %v", version, filename, err)
		http.NotFound(w, r)
		return
	}

	// Request the asset binary via the API URL with Accept: application/octet-stream
	req, err := http.NewRequest("GET", assetURL, nil)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	req.Header.Set("Accept", "application/octet-stream")
	if githubToken != "" {
		req.Header.Set("Authorization", "Bearer "+githubToken)
	}

	// GitHub will 302 to an S3 presigned URL — follow it without the auth header
	resp, err := noRedirectClient.Do(req)
	if err != nil {
		http.Error(w, "failed to fetch asset", http.StatusBadGateway)
		log.Printf("error fetching asset API URL %s: %v", assetURL, err)
		return
	}

	// If GitHub redirects to S3, follow the redirect without auth
	if resp.StatusCode == http.StatusFound || resp.StatusCode == http.StatusMovedPermanently {
		location := resp.Header.Get("Location")
		resp.Body.Close()
		if location == "" {
			http.Error(w, "upstream error", http.StatusBadGateway)
			return
		}
		resp, err = httpClient.Get(location)
		if err != nil {
			http.Error(w, "failed to download asset", http.StatusBadGateway)
			log.Printf("error following redirect to %s: %v", location, err)
			return
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		http.Error(w, "upstream error", http.StatusBadGateway)
		log.Printf("github returned %d for asset %s", resp.StatusCode, filename)
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	if resp.ContentLength > 0 {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", resp.ContentLength))
	}
	w.WriteHeader(http.StatusOK)

	if _, err := io.Copy(w, resp.Body); err != nil {
		log.Printf("error streaming %s: %v", filename, err)
	}
}

func main() {
	if githubToken == "" {
		log.Println("WARNING: GITHUB_TOKEN not set — private repo assets will not be accessible")
	}

	refreshLatestTag()
	go func() {
		for range time.Tick(5 * time.Minute) {
			refreshLatestTag()
		}
	}()

	mux := http.NewServeMux()

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	})

	mux.HandleFunc("/agent/latest/version", func(w http.ResponseWriter, r *http.Request) {
		tag := getLatestTag()
		if tag == "" {
			http.Error(w, "latest version not available", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintln(w, tag)
	})

	mux.HandleFunc("/agent/latest/", func(w http.ResponseWriter, r *http.Request) {
		filename := strings.TrimPrefix(r.URL.Path, "/agent/latest/")
		if filename == "" || filename == "version" {
			return // handled by more specific route
		}
		tag := getLatestTag()
		if tag == "" {
			http.Error(w, "latest version not available", http.StatusServiceUnavailable)
			return
		}
		proxyAsset(w, r, tag, filename)
	})

	mux.HandleFunc("/agent/", func(w http.ResponseWriter, r *http.Request) {
		// /agent/{version}/{filename}
		path := strings.TrimPrefix(r.URL.Path, "/agent/")
		parts := strings.SplitN(path, "/", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			http.NotFound(w, r)
			return
		}
		version, filename := parts[0], parts[1]
		if version == "latest" {
			return // handled by /agent/latest/ handler
		}
		proxyAsset(w, r, version, filename)
	})

	addr := ":8080"
	log.Printf("releases proxy listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}
