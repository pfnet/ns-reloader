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
	"testing"
	"time"

	"github.com/go-logr/logr/testr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

func TestReloader(t *testing.T) {
	testEnv := &envtest.Environment{}
	cfg, err := testEnv.Start()
	require.NoError(t, err)
	defer func() {
		assert.NoError(t, testEnv.Stop())
	}()
	k8sClient, err := client.New(cfg, client.Options{})
	require.NoError(t, err, "Failed to create k8s client")

	createTestNamespace := func(name string) {
		assert.NoError(t, k8sClient.Create(context.Background(), &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:   name,
				Labels: map[string]string{"organization/name": "test"},
			},
		}))
	}

	targetEnvVar := "WATCH_NAMESPACES"

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		var expectedNSs []string
		for i := range 10 {
			name := fmt.Sprintf("org-test--ns%d", i)
			createTestNamespace(name)
			expectedNSs = append(expectedNSs, name)
		}
		t.Log("All expected namespaces have been created.")

		assert.Eventually(t, func() bool {
			storedWNS := os.Getenv(targetEnvVar)
			expectedWNS := strings.Join(expectedNSs, ",")
			return expectedWNS == storedWNS
		}, time.Second*3, time.Millisecond*500, "Timed out waiting for expected namespaces to be set")
		cancel()
	}()

	pmCfg := &ProcessManagerConfig{
		kubeConfig:        cfg,
		namespaceSelector: "organization/name=test",
		targetEnvVar:      targetEnvVar,

		arguments: []string{"sh", "-c", "trap 'exit' TERM; while :; do sleep 1; done"},

		terminationGracePeriod: 1 * time.Second,
		debouncePeriod:         500 * time.Millisecond,
		logger:                 testr.New(t),
	}

	err = run(ctx, pmCfg)

	assert.NoError(t, err)
}
