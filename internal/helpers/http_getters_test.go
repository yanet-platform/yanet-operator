/*
Copyright 2023-2026 YANDEX LLC.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package helpers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHttpGet_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("test-response"))
	}))
	defer server.Close()

	result, err := HttpGet(server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result != "test-response" {
		t.Errorf("got %q, want %q", result, "test-response")
	}
}

func TestHttpGet_ErrorStatus(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
	}{
		{
			name:       "404 Not Found",
			statusCode: http.StatusNotFound,
			body:       "not found",
		},
		{
			name:       "500 Internal Server Error",
			statusCode: http.StatusInternalServerError,
			body:       "server error",
		},
		{
			name:       "403 Forbidden",
			statusCode: http.StatusForbidden,
			body:       "forbidden",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.statusCode)
				w.Write([]byte(tt.body))
			}))
			defer server.Close()

			_, err := HttpGet(server.URL)
			if err == nil {
				t.Fatal("expected error, got nil")
			}

			if !strings.Contains(err.Error(), "http") {
				t.Errorf("error should contain 'http', got: %v", err)
			}
		})
	}
}

func TestHttpGet_NetworkError(t *testing.T) {
	// Use invalid URL to trigger network error
	_, err := HttpGet("http://invalid-host-that-does-not-exist-12345.local")
	if err == nil {
		t.Fatal("expected error for invalid host, got nil")
	}
}

func TestHttpGet_Timeout(t *testing.T) {
	// This test would take 30+ seconds to run, so we skip it in normal test runs
	// It's here for documentation purposes
	t.Skip("Skipping timeout test as it takes 30+ seconds")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(35 * time.Second) // Longer than httpClient timeout (30s)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	_, err := HttpGet(server.URL)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}

	if !strings.Contains(err.Error(), "timeout") && !strings.Contains(err.Error(), "deadline") {
		t.Errorf("expected timeout/deadline error, got: %v", err)
	}
}

func TestHttpGet_EmptyResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		// No body
	}))
	defer server.Close()

	result, err := HttpGet(server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result != "" {
		t.Errorf("got %q, want empty string", result)
	}
}
