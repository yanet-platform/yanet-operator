package helpers

import (
	"fmt"
	"io"
	"net/http"
	"time"
)

// httpClient is a shared HTTP client with a reasonable timeout
// to prevent goroutines from blocking indefinitely on slow/hanging servers.
var httpClient = &http.Client{
	Timeout: 30 * time.Second,
}

// HttpGet makes a simple GET request and returns the string result.
// Returns (result, error) following Go conventions.
func HttpGet(uri string) (string, error) {
	resp, err := httpClient.Get(uri)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("HTTP %d from %s: %s", resp.StatusCode, uri, string(body))
	}

	return string(body), nil
}
