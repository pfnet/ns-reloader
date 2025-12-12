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
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func run(ctx context.Context, log logr.Logger, cmds []string, cfg *rest.Config) error {
	_, ok := os.LookupEnv(EnvKey_TargetEnvKey)
	if !ok {
		return fmt.Errorf("environment variable %s is not set", EnvKey_TargetEnvKey)
	}
	nsLbslStr, ok := os.LookupEnv(EnvKey_WatchNamespaceSelector)
	if !ok {
		return fmt.Errorf("environment variable %s is not set", EnvKey_WatchNamespaceSelector)
	}

	if cfg == nil {
		cfg = config.GetConfigOrDie()
	}
	mgr, err := manager.New(cfg, manager.Options{
		Metrics:                server.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0", // Disable metrics and health check endpoints. Wapped process should be responsible for them.
	})
	if err != nil {
		return fmt.Errorf("create manager: %w", err)
	}

	procMgr := &ProcessManager{
		log:        log,
		commands:   cmds,
		updateChan: make(chan struct{}, 1),
	}
	if err := mgr.Add(procMgr); err != nil {
		return fmt.Errorf("failed to add process manager: %w", err)
	}

	nsLbsl, err := metav1.ParseToLabelSelector(nsLbslStr)
	if err != nil {
		return fmt.Errorf("parse label selector from %s: %w", nsLbslStr, err)
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

type ProcessManager struct {
	log      logr.Logger
	commands []string
	cmd      *exec.Cmd

	watchNamespaces atomic.Value
	updateChan      chan struct{}
}

func (pm *ProcessManager) UpdateNamespaces(ns string) {
	pm.watchNamespaces.Store(ns) // overwrite with the latest status
	select {
	default:
	case pm.updateChan <- struct{}{}:
	}
}

func (pm *ProcessManager) Start(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			pm.log.Info("Shutting down process manager")
			return pm.stopProcess()
		case <-pm.updateChan:
			curNS := os.Getenv(EnvKey_TargetEnvKey)
			newNS := pm.watchNamespaces.Load().(string)
			if curNS == newNS {
				continue // already updated
			}
			if err := os.Setenv(EnvKey_TargetEnvKey, newNS); err != nil {
				return fmt.Errorf("failed to set %s: %w", EnvKey_TargetEnvKey, err)
			}
			if err := pm.stopProcess(); err != nil {
				return err
			}
			if err := pm.startProcess(); err != nil {
				return err
			}
			time.Sleep(processDebouncePeriod)
		}
	}
}

func (pm *ProcessManager) startProcess() error {
	if pm.cmd != nil {
		return fmt.Errorf("process is already running, previous termination might not have completed")
	}
	pm.cmd = exec.Command(pm.commands[0], pm.commands[1:]...)
	pm.cmd.Stdout = os.Stdout
	pm.cmd.Stderr = os.Stderr
	if err := pm.cmd.Start(); err != nil {
		return fmt.Errorf("failed to start process: %w", err)
	}
	pm.log.Info("Process started", "pid", pm.cmd.Process.Pid, "namespaces", os.Getenv("WATCH_NAMESPACE"))
	return nil
}

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
	case <-time.After(processTerminationGracePeriod):
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

type NamespaceWatcher struct {
	client         client.Client
	nsSelector     labels.Selector
	processManager *ProcessManager
}

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

	r.processManager.UpdateNamespaces(strings.Join(namespaces, ","))
	return reconcile.Result{}, nil
}
