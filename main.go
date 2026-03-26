package main

import (
	"encoding/json"
	"fmt"
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
)

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

type githubRelease struct {
	TagName string `json:"tag_name"`
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

	resp, err := http.DefaultClient.Do(req)
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

func downloadURL(version, filename string) string {
	return fmt.Sprintf("https://github.com/%s/%s/releases/download/%s/%s",
		githubOrg, githubRepo, version, filename)
}

func main() {
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
		http.Redirect(w, r, downloadURL(tag, filename), http.StatusFound)
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
		http.Redirect(w, r, downloadURL(version, filename), http.StatusFound)
	})

	addr := ":8080"
	log.Printf("releases proxy listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}
