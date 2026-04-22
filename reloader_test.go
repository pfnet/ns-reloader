// Copyright 2025 Preferred Networks, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/go-logr/logr/testr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

func testProcessReloader(t *testing.T, cfg *rest.Config, orgName string, cmdArgs []string) {
	k8sClient, err := client.New(cfg, client.Options{})
	require.NoError(t, err, "Failed to create k8s client")

	createTestNamespace := func(name string) {
		createErr := k8sClient.Create(context.Background(), &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:   name,
				Labels: map[string]string{"organization/name": orgName},
			},
		})
		assert.NoError(t, createErr)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		var expectedNSs []string
		for i := range 10 {
			name := fmt.Sprintf("org-%s--ns%d", orgName, i)
			createTestNamespace(name)
			expectedNSs = append(expectedNSs, name)
		}
		t.Log("All expected namespaces have been created.")

		assert.Eventually(t, func() bool {
			storedWNS := os.Getenv(defaultTargetEnvVar)
			expectedWNS := strings.Join(expectedNSs, ",")
			return expectedWNS == storedWNS
		}, time.Second*3, time.Millisecond*500, "Timed out waiting for expected namespaces to be set")
		cancel()
	}()

	pmCfg := &ProcessManagerConfig{
		kubeConfig:        cfg,
		namespaceSelector: fmt.Sprintf("organization/name=%s", orgName),
		targetEnvVar:      defaultTargetEnvVar,

		arguments:              cmdArgs,
		terminationGracePeriod: 2 * time.Second,
		sigkillTimeout:         2 * time.Second,
		debouncePeriod:         500 * time.Millisecond,
		logger:                 testr.New(t),
	}

	err = run(ctx, orgName, pmCfg)

	assert.NoError(t, err)
}

func testProcessRestart(t *testing.T, orgName string, cmdArgs []string) {
	pm := NewProcessManager(&ProcessManagerConfig{
		// not used
		kubeConfig: nil,

		// not used
		namespaceSelector: fmt.Sprintf("organization/name=%s", orgName),

		// not used
		targetEnvVar: defaultTargetEnvVar,

		arguments:              cmdArgs,
		terminationGracePeriod: 2 * time.Second,
		sigkillTimeout:         2 * time.Second,
		debouncePeriod:         500 * time.Millisecond,
		logger:                 testr.New(t),
	})

	pmStop := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		assert.NoError(t, pm.Start(ctx))
		close(pmStop)
	}()

	// Ensure the process manager is stopped at the end of the test.
	defer func() {
		// Signal the process manager to stop.
		cancel()

		// Wait for the process manager to stop
		<-pmStop
	}()

	// Set the initial namespaces to trigger process start.
	pm.UpdateNamespaces("initial-namespace")

	// Wait for the initial process to start.
	var oldPid int
	require.Eventually(t, func() bool {
		pm.process.RLock()
		defer pm.process.RUnlock()

		// Check if the process has started.
		select {
		case <-pm.Done():
			return false
		default:
		}

		oldPid = pm.currentPID()
		// Sanity check on the PID.
		return oldPid != 0
	}, 5*time.Second, 500*time.Millisecond, "Timed out waiting initial process to start")

	t.Log("Initial process started with PID", oldPid)

	// Kill the process.
	func() {
		pm.process.RLock()
		defer pm.process.RUnlock()
		require.NotNil(t, pm.process.cmd)
		require.NoError(t, pm.process.cmd.Process.Signal(syscall.SIGTERM))
	}()

	// Check for the restarted process.
	assert.Eventually(t, func() bool {
		select {
		case <-pm.Done():
			t.Log("Old process is stopped", oldPid)
			return false
		default:
		}

		newPid := pm.currentPID()
		if newPid == oldPid {
			t.Log("Old process is still running", oldPid)
			return false
		}

		t.Log("New process has started", newPid)
		return true
	}, 5*time.Second, 500*time.Millisecond, "Timed out waiting for the process to restart")
}

func testDebounce(t *testing.T, orgName string, cmdArgs []string) {
	pm := NewProcessManager(&ProcessManagerConfig{
		// not used
		kubeConfig: nil,

		// not used
		namespaceSelector: fmt.Sprintf("organization/name=%s", orgName),

		// not used
		targetEnvVar: defaultTargetEnvVar,

		arguments:              cmdArgs,
		terminationGracePeriod: 2 * time.Second,
		sigkillTimeout:         2 * time.Second,
		debouncePeriod:         2 * time.Second,
		logger:                 testr.New(t),
	})

	pmStop := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		assert.NoError(t, pm.Start(ctx))
		close(pmStop)
	}()

	// Ensure the process manager is stopped at the end of the test.
	defer func() {
		// Signal the process manager to stop.
		cancel()

		// Wait for the process manager to stop
		<-pmStop
	}()

	// Set the initial namespaces to trigger process start.
	startingNS := "initial-namespace"
	pm.UpdateNamespaces(startingNS)

	// Wait for the initial process to start.
	var oldPid int
	require.Eventually(t, func() bool {
		pm.process.RLock()
		defer pm.process.RUnlock()

		// Check if the process has started.
		select {
		case <-pm.Done():
			return false
		default:
		}

		oldPid = pm.currentPID()
		// Sanity check on the PID.
		return oldPid != 0
	}, 5*time.Second, 500*time.Millisecond, "Timed out waiting initial process to start")

	t.Log("Initial process started with PID", oldPid)

	numUpdates := 50
	expectedWNS := fmt.Sprintf("namespace-%d", numUpdates-1)
	envHistory := make(chan string, numUpdates+1)
	pidHistory := make(chan int, numUpdates+1)
	observeDone := make(chan struct{})
	go func() {
		defer close(observeDone)

		prevWNS := os.Getenv(defaultTargetEnvVar)
		prevPID := pm.currentPID()
		for {
			curWNS := os.Getenv(defaultTargetEnvVar)
			curPID := pm.currentPID()
			if curWNS != prevWNS {
				envHistory <- curWNS
				prevWNS = curWNS
			}
			if curPID != 0 && curPID != prevPID {
				pidHistory <- curPID
				prevPID = curPID
			}
			if curWNS == expectedWNS && curPID != 0 && curPID != oldPid {
				// We've observed the expected final state, we can stop observing now.
				return
			}

			select {
			case <-ctx.Done():
				return
			case <-time.After(10 * time.Millisecond):
			}
		}
	}()

	// Rapidly update namespaces.
	go func() {
		// ~500 millisecond worth of updates
		for i := range numUpdates {
			pm.UpdateNamespaces(fmt.Sprintf("namespace-%d", i))
			time.Sleep(10 * time.Millisecond)
		}
	}()

	select {
	case <-observeDone:
	case <-time.After(5 * time.Second):
		t.Fatal("expected to observe the expected final state within the timeout")
	}
	close(envHistory)
	close(pidHistory)

	var transitions []string
	for wns := range envHistory {
		transitions = append(transitions, wns)
	}
	assert.Equal(t, []string{expectedWNS}, transitions, "expected only the final debounced namespace to be applied")

	var pidTransitions []int
	for pid := range pidHistory {
		pidTransitions = append(pidTransitions, pid)
	}
	assert.Len(t, pidTransitions, 1, "expected exactly one debounced process restart")
	assert.NotEqual(t, oldPid, pidTransitions[0], "expected the debounced restart to use a new PID")
}

func TestProcessManagerIgnoresDuplicateNamespaceUpdates(t *testing.T) {
	pm := NewProcessManager(&ProcessManagerConfig{
		targetEnvVar: defaultTargetEnvVar,
		logger:       testr.New(t),
	})

	pm.UpdateNamespaces("ns-a")
	select {
	case <-pm.updateChan:
	default:
		t.Fatal("expected first namespace update to be queued")
	}

	pm.UpdateNamespaces("ns-a")
	select {
	case <-pm.updateChan:
		t.Fatal("duplicate namespace update should not be queued")
	default:
	}
}

func TestReloader(t *testing.T) {
	testEnv := &envtest.Environment{}
	cfg, err := testEnv.Start()
	require.NoError(t, err)
	defer func() {
		assert.NoError(t, testEnv.Stop())
	}()

	t.Run("handles process responding to SIGTERM", func(t *testing.T) {
		args := []string{"sh", "-c", "trap 'exit' TERM; while :; do sleep 1; done"}
		testProcessReloader(t, cfg, "test-sigterm", args)
	})

	t.Run("handles process responding to SIGKILL", func(t *testing.T) {
		args := []string{"sh", "-c", "trap '' TERM; while :; do sleep 1; done"}
		testProcessReloader(t, cfg, "test-sigkill", args)
	})
}

func TestProcessManager(t *testing.T) {
	t.Run("handles process restarts", func(t *testing.T) {
		args := []string{"sh", "-c", "trap 'exit' TERM; while :; do sleep 1; done"}
		testProcessRestart(t, "test-restart", args)
	})

	t.Run("handles debounce", func(t *testing.T) {
		args := []string{"sh", "-c", "trap 'exit' TERM; while :; do sleep 1; done"}
		testDebounce(t, "test-debounce", args)
	})
}
