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

package controller

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
)

// TestCheckUpdateRequeue tests the checkUpdateRequeue method
func TestCheckUpdateRequeue(t *testing.T) {
	tests := []struct {
		name             string
		updateWindow     time.Duration
		updateHost       string
		lastUpdateHost   string
		lastUpdateTS     time.Time
		expectRequeue    bool
		expectRetryDelay bool
	}{
		{
			name:             "no update window",
			updateWindow:     0,
			updateHost:       "host1",
			lastUpdateHost:   "",
			lastUpdateTS:     time.Time{},
			expectRequeue:    false,
			expectRetryDelay: false,
		},
		{
			name:             "first update",
			updateWindow:     5 * time.Minute,
			updateHost:       "host1",
			lastUpdateHost:   "",
			lastUpdateTS:     time.Time{},
			expectRequeue:    false,
			expectRetryDelay: false,
		},
		{
			name:             "same host update allowed",
			updateWindow:     5 * time.Minute,
			updateHost:       "host1",
			lastUpdateHost:   "host1",
			lastUpdateTS:     time.Now().Add(-1 * time.Minute),
			expectRequeue:    false,
			expectRetryDelay: false,
		},
		{
			name:             "different host too early",
			updateWindow:     5 * time.Minute,
			updateHost:       "host2",
			lastUpdateHost:   "host1",
			lastUpdateTS:     time.Now().Add(-1 * time.Minute),
			expectRequeue:    true,
			expectRetryDelay: true,
		},
		{
			name:             "different host window expired",
			updateWindow:     5 * time.Minute,
			updateHost:       "host2",
			lastUpdateHost:   "host1",
			lastUpdateTS:     time.Now().Add(-6 * time.Minute),
			expectRequeue:    false,
			expectRetryDelay: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create reconciler with test state
			r := &YanetReconciler{
				lock:           sync.Mutex{},
				lastUpdateHost: tt.lastUpdateHost,
				lastUpdateTS:   tt.lastUpdateTS,
			}

			// Call checkUpdateRequeue
			logger := logr.Discard()
			retryDelay := r.checkUpdateRequeue(logger, tt.updateWindow, tt.updateHost)

			// Check if retry delay is set
			hasRetryDelay := retryDelay > 0
			if hasRetryDelay != tt.expectRetryDelay {
				t.Errorf("checkUpdateRequeue() retryDelay > 0 = %v, want %v (delay: %v)",
					hasRetryDelay, tt.expectRetryDelay, retryDelay)
			}

			// If no retry expected and updateWindow > 0, verify state was updated
			if !tt.expectRequeue && tt.updateWindow > 0 {
				if r.lastUpdateHost != tt.updateHost {
					t.Errorf("lastUpdateHost = %v, want %v", r.lastUpdateHost, tt.updateHost)
				}
			}
		})
	}
}

// TestCheckUpdateRequeue_Concurrency tests thread safety of checkUpdateRequeue
func TestCheckUpdateRequeue_Concurrency(t *testing.T) {
	r := &YanetReconciler{
		lock:           sync.Mutex{},
		lastUpdateHost: "",
		lastUpdateTS:   time.Time{},
	}

	logger := logr.Discard()
	updateWindow := 5 * time.Minute

	// Run concurrent updates
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(hostNum int) {
			defer wg.Done()
			host := fmt.Sprintf("host%d", hostNum)
			r.checkUpdateRequeue(logger, updateWindow, host)
		}(i)
	}

	wg.Wait()

	// Verify state is consistent (no data race)
	if r.lastUpdateHost == "" {
		t.Error("lastUpdateHost should be set after concurrent updates")
	}
	if r.lastUpdateTS.IsZero() {
		t.Error("lastUpdateTS should be set after concurrent updates")
	}
}
