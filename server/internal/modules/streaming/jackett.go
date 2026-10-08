package streaming

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

type JackettResult struct {
	Title        string `json:"Title"`
	MagnetUri    string `json:"MagnetUri"`
	Seeders      int    `json:"Seeders"`
	Peers        int    `json:"Peers"`
	Size         int64  `json:"Size"`
	CategoryDesc string `json:"CategoryDesc"`
	Tracker      string `json:"Tracker"`
	Link         string `json:"Link"` // Jackett download URL (.torrent or redirect to a magnet)
	InfoHash     string `json:"InfoHash"`
	IMDbMatch    bool   `json:"-"` // found by an IMDb id search, so it is the title whatever its name
}

type JackettResponse struct {
	Results []JackettResult `json:"Results"`
}

type TorrentSearcher struct {
	jackettURL    string
	jackettAPIKey string
	minSeeders    int
}

func NewTorrentSearcher(jackettURL, apiKey string, minSeeders int) *TorrentSearcher {
	return &TorrentSearcher{
		jackettURL:    jackettURL,
		jackettAPIKey: apiKey,
		minSeeders:    minSeeders,
	}
}

// JackettURL returns the Jackett base URL this searcher uses
func (ts *TorrentSearcher) JackettURL() string { return ts.jackettURL }

// SearchJackettRaw returns the unfiltered Jackett results (including ones without a magnet).
func (ts *TorrentSearcher) SearchJackettRaw(ctx context.Context, query string, categories ...string) ([]JackettResult, error) {
	if ts.jackettURL == "" || ts.jackettAPIKey == "" {
		return nil, fmt.Errorf("jackett is not configured")
	}

	params := url.Values{}
	params.Set("apikey", ts.jackettAPIKey)
	params.Set("Query", query)
	for _, cat := range categories {
		params.Add("Category[]", cat)
	}
	u := fmt.Sprintf("%s/api/v2.0/indexers/all/results?%s", strings.TrimSuffix(ts.jackettURL, "/"), params.Encode())

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		// Drop the request URL from the error, it contains the API key
		var ue *url.Error
		if errors.As(err, &ue) {
			return nil, fmt.Errorf("jackett request failed: %w", ue.Err)
		}
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("jackett returned status %d", resp.StatusCode)
	}

	var jackettResp JackettResponse
	if err := json.NewDecoder(resp.Body).Decode(&jackettResp); err != nil {
		return nil, err
	}
	return jackettResp.Results, nil
}
