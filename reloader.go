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

func run(ctx context.Context, cfg *ProcessManagerConfig) error {
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
	debouncePeriod         time.Duration
}

// ProcessManager manages the lifecycle of a wrapped process,
type ProcessManager struct {
	log logr.Logger

	targetEnvVar string

	arguments              []string
	terminationGracePeriod time.Duration
	debouncePeriod         time.Duration

	cmd             *exec.Cmd
	watchNamespaces atomic.Value
	updateChan      chan struct{}
}

// NewProcessManager creates a new [ProcessManager] with the given configuration.
func NewProcessManager(cfg *ProcessManagerConfig) *ProcessManager {
	var watchNamespaces atomic.Value

	// NOTE: Initialize with empty string to avoid nil dereference.
	//       This should still be set by Reconcile before use.
	watchNamespaces.Store("")
	return &ProcessManager{
		arguments:              cfg.arguments,
		log:                    cfg.logger,
		targetEnvVar:           cfg.targetEnvVar,
		watchNamespaces:        watchNamespaces,
		terminationGracePeriod: cfg.terminationGracePeriod,
		debouncePeriod:         cfg.debouncePeriod,
		updateChan:             make(chan struct{}, 1),
	}
}

// UpdateNamespaces updates the namespaces to watch and triggers a process
// restart. This method may be called concurrently.
func (pm *ProcessManager) UpdateNamespaces(ns []string) {
	// Update the requested namespaces to watch.
	pm.watchNamespaces.Store(strings.Join(ns, ","))
	select {
	default:
	case pm.updateChan <- struct{}{}:
	}
}

// Start starts the process manager loop.
func (pm *ProcessManager) Start(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			pm.log.Info("Shutting down process manager")
			return pm.stopProcess()
		case <-pm.updateChan:
			curNS := os.Getenv(pm.targetEnvVar)
			newNS := pm.watchNamespaces.Load().(string)
			if curNS == newNS {
				continue // already current.
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
			time.Sleep(pm.debouncePeriod)
		}
	}
}

// startProcess starts the wrapped process. This method cannot be called
// concurrently.
func (pm *ProcessManager) startProcess() error {
	if pm.cmd != nil {
		return fmt.Errorf("process is already running, previous termination might not have completed")
	}
	pm.cmd = exec.Command(pm.arguments[0], pm.arguments[1:]...)
	pm.cmd.Stdout = os.Stdout
	pm.cmd.Stderr = os.Stderr

	if err := pm.cmd.Start(); err != nil {
		return fmt.Errorf("failed to start process: %w", err)
	}
	pm.log.Info("Process started", "pid", pm.cmd.Process.Pid, "namespaces", os.Getenv(pm.targetEnvVar))
	return nil
}

// stopProcess stops the wrapped process gracefully. This method cannot be
// called concurrently.
func (pm *ProcessManager) stopProcess() error {
	if pm.cmd == nil {
		return nil // no process is running
	}
	pid := pm.cmd.Process.Pid
	if err := pm.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		return fmt.Errorf("failed to send SIGTERM to pid %d: %w", pid, err)
	}
	done := make(chan error, 1)
	go func() { done <- pm.cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(pm.terminationGracePeriod):
		pm.log.Info("Process did not stop gracefully, sending SIGKILL", "pid", pid)
		if err := pm.cmd.Process.Signal(syscall.SIGKILL); err != nil {
			return fmt.Errorf("failed to send SIGKILL to pid %d: %w", pid, err)
		}
		<-done
	}
	pm.log.Info("Process stopped", "pid", pid)
	pm.cmd = nil
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
