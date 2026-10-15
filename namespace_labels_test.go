// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"os"
	"path/filepath"
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

func TestReloaderNamespaceLabelChanges(t *testing.T) {
	testEnv := &envtest.Environment{}
	cfg, err := testEnv.Start()
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, testEnv.Stop()) })
	k8sClient, err := client.New(cfg, client.Options{})
	require.NoError(t, err)

	for _, tc := range []struct {
		name, selector  string
		matching, other map[string]string
	}{
		{"positive", "watch=true", map[string]string{"watch": "true"}, map[string]string{"watch": "false"}},
		{"negative", "!watch", nil, map[string]string{"watch": "false"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
				Name: "label-change-" + tc.name, Labels: tc.matching,
			}}
			require.NoError(t, k8sClient.Create(ctx, ns))
			output := filepath.Join(t.TempDir(), "namespaces")
			const targetEnv = "NS_RELOADER_TEST_LABEL_CHANGES"
			t.Setenv(targetEnv, "")
			result := make(chan error, 1)
			go func() {
				result <- run(ctx, "label-changes-"+tc.name, &ProcessManagerConfig{
					kubeConfig: cfg, namespaceSelector: tc.selector, targetEnvVar: targetEnv,
					arguments:              []string{"sh", "-c", `printf '%s\n' "$NS_RELOADER_TEST_LABEL_CHANGES" >> "$1"; trap 'exit' TERM; while :; do sleep 0.1; done`, "reloader-test", output},
					terminationGracePeriod: time.Second, sigkillTimeout: time.Second,
					debouncePeriod: 10 * time.Millisecond, logger: testr.New(t),
				})
			}()
			t.Cleanup(func() {
				cancel()
				select {
				case err := <-result:
					assert.NoError(t, err)
				case <-time.After(5 * time.Second):
					t.Error("timed out stopping reloader")
				}
			})
			// Read the wrapped process's output, rather than the parent's environment,
			// to verify that the process restarted with the updated selection.
			waitForMembership := func(selected bool) {
				t.Helper()
				require.Eventually(t, func() bool {
					data, err := os.ReadFile(output)
					if err != nil || len(data) == 0 {
						return false
					}
					lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
					for _, name := range strings.Split(lines[len(lines)-1], ",") {
						if name == ns.Name {
							return selected
						}
					}
					return !selected
				}, 10*time.Second, 50*time.Millisecond, "wrapped process namespace membership should be %v", selected)
			}
			waitForMembership(true)
			require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(ns), ns))
			ns.Labels = tc.other
			require.NoError(t, k8sClient.Update(ctx, ns))
			waitForMembership(false)
			require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(ns), ns))
			ns.Labels = tc.matching
			require.NoError(t, k8sClient.Update(ctx, ns))
			waitForMembership(true)
		})
	}
}
