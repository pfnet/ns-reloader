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
	"os/exec"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func run(ctx context.Context, controllerName string, cfg *ProcessManagerConfig) error {
	mgr, err := manager.New(cfg.kubeConfig, manager.Options{
		Metrics: server.Options{BindAddress: "0"},
		// Disable metrics and health check endpoints.
		// Wrapped process should be responsible for them.
		HealthProbeBindAddress: "0",
	})
	if err != nil {
		return fmt.Errorf("create manager: %w", err)
	}

	procMgr := NewProcessManager(cfg)
	if addErr := mgr.Add(procMgr); addErr != nil {
		return fmt.Errorf("failed to add process manager: %w", addErr)
	}

	nsLbsl, err := metav1.ParseToLabelSelector(cfg.namespaceSelector)
	if err != nil {
		return fmt.Errorf("parse label selector from %s: %w", cfg.namespaceSelector, err)
	}
	nsLblPredicate, err := predicate.LabelSelectorPredicate(*nsLbsl)
	if err != nil {
		return fmt.Errorf("create namespace label predicate: %w", err)
	}
	nsSelector, err := metav1.LabelSelectorAsSelector(nsLbsl)
	if err != nil {
		return fmt.Errorf("convert label selector: %w", err)
	}
	if err := builder.ControllerManagedBy(mgr).
		Named(controllerName).
		For(&corev1.Namespace{}).
		WithEventFilter(nsLblPredicate).
		Complete(&NamespaceWatcher{
			client:         mgr.GetClient(),
			nsSelector:     nsSelector,
			processManager: procMgr,
		}); err != nil {
		return fmt.Errorf("build controller: %w", err)
	}

	return mgr.Start(ctx)
}

// ProcessManagerConfig holds the configuration for [ProcessManager].
type ProcessManagerConfig struct {
	logger    logr.Logger
	arguments []string

	kubeConfig *rest.Config

	namespaceSelector string
	targetEnvVar      string

	terminationGracePeriod time.Duration
	sigkillTimeout         time.Duration
	debouncePeriod         time.Duration
}

// ProcessManager manages the lifecycle of a wrapped process,
type ProcessManager struct {
	log logr.Logger

	targetEnvVar string

	arguments              []string
	terminationGracePeriod time.Duration
	sigkillTimeout         time.Duration
	debouncePeriod         time.Duration

	// process holds the state of the currently running process. Subfields are
	// protected by the embedded mutex.
	process struct {
		sync.RWMutex

		// cmd is the currently running command (sub-process). It is nil if no
		// process is running.
		cmd *exec.Cmd

		// done is closed when the process has exited.
		done chan struct{}
	}

	// watchNamespaces holds the currently requested namespaces to watch.
	watchNamespaces atomic.Value

	// updateChan is used to signal that the watched namespaces have changed.
	updateChan chan struct{}
}

// NewProcessManager creates a new [ProcessManager] with the given configuration.
func NewProcessManager(cfg *ProcessManagerConfig) *ProcessManager {
	var watchNamespaces atomic.Value

	// NOTE: Initialize with empty string to avoid nil dereference.
	//       This should still be set by Reconcile before use.
	watchNamespaces.Store("")

	pm := &ProcessManager{
		arguments:              cfg.arguments,
		log:                    cfg.logger,
		targetEnvVar:           cfg.targetEnvVar,
		watchNamespaces:        watchNamespaces,
		terminationGracePeriod: cfg.terminationGracePeriod,
		sigkillTimeout:         cfg.sigkillTimeout,
		debouncePeriod:         cfg.debouncePeriod,
		updateChan:             make(chan struct{}, 1),
	}

	// Initialize the process done channel as closed to indicate no process is
	// running.
	pm.process.done = make(chan struct{})
	close(pm.process.done)

	return pm
}

// UpdateNamespaces updates the namespaces to watch and triggers a process
// restart. This method may be called concurrently by Reconcile.
func (pm *ProcessManager) UpdateNamespaces(ns []string) {
	// Update the requested namespaces to watch.
	namespaces := strings.Join(ns, ",")
	pm.watchNamespaces.Store(namespaces)
	select {
	case pm.updateChan <- struct{}{}:
	default:
	}
}

// Start starts the process manager loop.
func (pm *ProcessManager) Start(ctx context.Context) error {
	// Start the wrapped process immediately.
	debounceTimer := time.NewTimer(0)
	// Prevent the timer from firing immediately. We need to wait for the
	// initial set of namespaces to be provided before starting the process.
	// As of Go 1.23, the C channel will block after Stop is called.
	debounceTimer.Stop()
	lastReload := time.Now()

	for {
		select {
		case <-ctx.Done():
			pm.log.Info("Shutting down process manager")
			return pm.stopProcess()
		case <-debounceTimer.C:
			curNS := os.Getenv(pm.targetEnvVar)
			newNS := pm.watchNamespaces.Load().(string)

			select {
			case <-pm.Done():
				// Process is not running. Restart is needed.
			default:
				// Process is running. If the namespaces are unchanged, do
				// nothing.
				if curNS == newNS {
					continue // already current.
				}
			}

			// Update the currently watched namespaces.
			if err := os.Setenv(pm.targetEnvVar, newNS); err != nil {
				return fmt.Errorf("failed to set %s: %w", pm.targetEnvVar, err)
			}
			if err := pm.stopProcess(); err != nil {
				return err
			}
			if err := pm.startProcess(); err != nil {
				return err
			}

			lastReload = time.Now()
		case <-pm.updateChan:
			var waitPeriod time.Duration
			select {
			case <-pm.Done():
				// Process is not running. Restart immediately.
				waitPeriod = 0
			default:
				// Process is running. Wait for the debounce period to elapse
				// since the last reload.
				waitPeriod = max(pm.debouncePeriod-time.Since(lastReload), 0)
			}
			if !debounceTimer.Reset(waitPeriod) {
				// The timer has already triggered. Create a new timer.
				debounceTimer = time.NewTimer(waitPeriod)
			}
			pm.log.Info("Namespace update received; scheduling restart", "waitPeriod", waitPeriod)
		}
	}
}

// currentPID returns the PID of the currently running process, or 0 if no
// process is running.
func (pm *ProcessManager) currentPID() int {
	pm.process.RLock()
	defer pm.process.RUnlock()
	if pm.process.cmd == nil {
		return 0
	}
	return pm.process.cmd.Process.Pid
}

// Done returns a channel that is closed when the wrapped process exits.
func (pm *ProcessManager) Done() chan struct{} {
	pm.process.RLock()
	defer pm.process.RUnlock()
	return pm.process.done
}

// startProcess starts the wrapped process and returns immediately.
func (pm *ProcessManager) startProcess() error {
	pm.process.Lock()
	defer pm.process.Unlock()

	// This may occur if startProcess is called concurrently with
	// `waitForProcess`. This shouldn't happen.
	if pm.process.cmd != nil {
		panic(fmt.Sprintf("process %d is already started; this is a bug, please submit an issue at %s", pm.process.cmd.Process.Pid, issuesURL))
	}

	pm.process.cmd = exec.Command(pm.arguments[0], pm.arguments[1:]...)
	pm.process.cmd.Stdout = os.Stdout
	pm.process.cmd.Stderr = os.Stderr

	if err := pm.process.cmd.Start(); err != nil {
		return fmt.Errorf("failed to start process: %w", err)
	}

	pm.process.done = make(chan struct{})

	pm.log.Info("Process started", "pid", pm.process.cmd.Process.Pid, "namespaces", os.Getenv(pm.targetEnvVar))

	go pm.waitForProcess(pm.process.cmd, pm.process.done)

	return nil
}

// waitForProcess waits for the wrapped process to exit and cleans up the
// state accordingly. This method will run concurrently with
// [ProcessManager.startProcess] and [ProcessManager.stopProcess].
func (pm *ProcessManager) waitForProcess(cmd *exec.Cmd, done chan struct{}) {
	pm.log.Info("Waiting for process", "pid", cmd.Process.Pid)

	err := cmd.Wait()
	if err != nil {
		pm.log.Error(err, "Process exited with error", "pid", cmd.Process.Pid)
	} else {
		pm.log.Info("Process exited", "pid", cmd.Process.Pid)
	}

	// The process has terminated. Clean up the cmd resource.
	pm.process.Lock()
	pm.process.cmd = nil
	pm.process.Unlock()

	// Signal that the process has exited.
	close(done)

	// Request that the process be restarted.
	select {
	case pm.updateChan <- struct{}{}:
	default:
	}
}

// stopProcess stops the wrapped process gracefully. stopProcess should not be
// running concurrently with [ProcessManager.startProcess].
func (pm *ProcessManager) stopProcess() error {
	select {
	case <-pm.Done():
		// Process is already stopped.
		return nil
	default:
	}

	pid := pm.currentPID()

	pm.log.Info("Stopping process", "pid", pid)
	if err := pm.sendSignal(syscall.SIGTERM); err != nil {
		return err
	}

	select {
	case <-pm.Done():
		// Process is already stopped.
		return nil
	case <-time.After(pm.terminationGracePeriod):
	}

	pm.log.Info("Process did not stop gracefully, sending SIGKILL", "pid", pid)
	if err := pm.sendSignal(syscall.SIGKILL); err != nil {
		return err
	}

	select {
	case <-pm.Done():
		// Process is already stopped.
		return nil
	case <-time.After(pm.sigkillTimeout):
	}

	// The process is not responding to SIGKILL. Completely bail out.
	return fmt.Errorf("process %d did not respond to SIGKILL after %s", pid, pm.sigkillTimeout)
}

// sendSignal sends the given signal to terminate the wrapped process. If the
// process is not running, this is a no-op.
func (pm *ProcessManager) sendSignal(sig os.Signal) error {
	pm.process.RLock()
	defer pm.process.RUnlock()
	if pm.process.cmd == nil {
		return nil
	}

	if err := pm.process.cmd.Process.Signal(sig); err != nil {
		if processExistsErr := pm.process.cmd.Process.Signal(syscall.Signal(0)); processExistsErr == nil {
			// The process exists but we failed to send a signal.
			// Something very wrong has happened.
			return fmt.Errorf("failed to send %v to pid %d: %w", sig, pm.process.cmd.Process.Pid, err)
		}
	}

	return nil
}

// NamespaceWatcher watches namespace resources and notifies the ProcessManager
type NamespaceWatcher struct {
	client         client.Client
	nsSelector     labels.Selector
	processManager *ProcessManager
}

// Reconcile reconciles the namespace resources.
func (r *NamespaceWatcher) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	var nslist corev1.NamespaceList
	if err := r.client.List(ctx, &nslist, &client.ListOptions{
		LabelSelector: r.nsSelector,
	}); err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to list namespaces: %w", err)
	}

	var namespaces []string
	for _, ns := range nslist.Items {
		namespaces = append(namespaces, ns.Name)
	}
	sort.Strings(namespaces)

	r.processManager.UpdateNamespaces(namespaces)
	return reconcile.Result{}, nil
}
