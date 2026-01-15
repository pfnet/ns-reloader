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
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/go-logr/logr"
	"go.uber.org/zap/zapcore"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

var (
	// version and commit are set via ldflags during build
	version = "dev"
	commit  = "unknown"
)

const (
	// envKeyWatchNamespaceSelector is the environment variable
	// specifying the label selector for namespaces to watch by reloader.
	envKeyWatchNamespaceSelector = "RELOADER_NAMESPACE_SELECTOR"

	// envKeyTargetEnvKey is the environment variable where the list of
	// watched namespaces will be set for the wrapped process.
	envKeyTargetEnvKey = "RELOADER_TARGET_ENV_KEY"

	// envKeyTerminationGracePeriod is the environment variable
	// specifying the termination grace period for the wrapped process.
	envKeyTerminationGracePeriod = "RELOADER_TERMINATION_GRACE_PERIOD"

	// envKeySigkillTimeout is the environment variable specifying the time to
	// wait for the process after SIGKILL has been sent before completely
	// giving up.
	envKeySigkillTimeout = "RELOADER_SIGKILL_TIMEOUT"

	// envKeyDebouncePeriod is the environment variable specifying the
	// debounce period for handling namespace changes.
	envKeyDebouncePeriod = "RELOADER_DEBOUNCE_PERIOD"

	repoURL   = "https://github.com/pfnet/ns-reloader"
	issuesURL = repoURL + "/issues"
)

const (
	// defaultTargetEnvVar is the default environment variable name
	// to set the list of watched namespaces for the wrapped process.
	defaultTargetEnvVar = "WATCH_NAMESPACES"

	// defaultTerminationGracePeriod is the default grace period for terminating
	// the wrapped process. It is the time between sending the termination
	// signal and forcefully killing the process.
	defaultTerminationGracePeriod = 5 * time.Second

	// defaultSigkillTimeout is the default time to wait for the process after
	// SIGKILL has been sent before completely giving up.
	defaultSigkillTimeout = 5 * time.Second

	// defaultDebouncePeriod is the default debounce period for handling
	// namespace changes. It is the time to wait after a change before
	// restarting the wrapped process.
	defaultDebouncePeriod = 5 * time.Second
)

func getEnvDefault(key, defaultVal string) string {
	val := os.Getenv(key)
	if val == "" {
		return defaultVal
	}
	return val
}

func getEnvDuration(log logr.Logger, key string, defaultVal time.Duration) time.Duration {
	valStr := os.Getenv(key)
	if valStr == "" {
		return defaultVal
	}
	val, err := time.ParseDuration(valStr)
	if err != nil {
		log.Info("Failed to parse duration from env; using default", "key", key, "value", valStr)
		val = defaultVal
	}
	return val
}

func main() {
	log := zap.New(zap.UseFlagOptions(&zap.Options{
		Development: true,
		TimeEncoder: zapcore.RFC3339TimeEncoder,
		Level:       zapcore.InfoLevel,
	})).WithName("reloader")

	flagSet := flag.NewFlagSet(os.Args[0], flag.ExitOnError)
	namespaceSelector := flagSet.String(
		"namespace-selector",
		getEnvDefault(envKeyWatchNamespaceSelector, ""),
		"Label selector for namespaces to watch",
	)
	targetEnvName := flagSet.String(
		"target-envvar",
		getEnvDefault(envKeyTargetEnvKey, defaultTargetEnvVar),
		"Environment variable name to set the list of watched namespaces for the command",
	)
	terminationGracePeriod := flagSet.Duration(
		"termination-grace-period",
		getEnvDuration(log, envKeyTerminationGracePeriod, defaultTerminationGracePeriod),
		"Grace period for terminating the wrapped process",
	)
	sigkillTimeout := flagSet.Duration(
		"sigkill-timeout",
		getEnvDuration(log, envKeySigkillTimeout, defaultSigkillTimeout),
		"Timeout to wait after sending SIGKILL before giving up.",
	)
	debouncePeriod := flagSet.Duration(
		"debounce-period",
		getEnvDuration(log, envKeyDebouncePeriod, defaultDebouncePeriod),
		"Debounce period for handling namespace changes",
	)
	// kubeconfig path flag for out-of-cluster configuration.
	// NOTE: This is read by
	//       [sigs.k8s.io/controller-runtime/pkg/client/config.GetConfig] so we
	//       don't use it directly.
	_ = flagSet.String(
		config.KubeconfigFlagName,
		"",
		"Path to the kubeconfig file to use",
	)
	config.RegisterFlags(flagSet)

	if err := flagSet.Parse(os.Args[1:]); err != nil {
		log.Error(err, "Failed to parse flags")
		flagSet.Usage()
		os.Exit(1)
	}

	args := flagSet.Args()

	if len(args) == 0 {
		log.Error(nil, "No command specified")
		flagSet.Usage()
		os.Exit(1)
	}

	// Ensure namespace selector is specified. Watching across all namespaces
	// should typically be done directly by the controller with cluster scope.
	if *namespaceSelector == "" {
		log.Error(
			nil,
			"Namespace label selector must be specified. "+
				"If you wish to watch all namespaces, "+
				fmt.Sprintf("you likely do not want to use %s. ", os.Args[0])+
				"Instead, configure your controller to watch with cluster scope.",
		)
		flagSet.Usage()
		os.Exit(1)
	}

	ctrl.SetLogger(log)
	log.Info("Starting ns-reloader", "version", version, "commit", commit)
	log.Info("Configuration",
		"namespaceSelector", *namespaceSelector,
		"targetEnvVar", *targetEnvName,
		"terminationGracePeriod", terminationGracePeriod.String(),
		"debouncePeriod", debouncePeriod.String(),
		"command", args,
	)

	kubeConfig, err := config.GetConfig()
	if err != nil {
		log.Error(err, "Failed to load kubeconfig")
	}

	cfg := &ProcessManagerConfig{
		kubeConfig:             kubeConfig,
		logger:                 log,
		arguments:              args,
		namespaceSelector:      *namespaceSelector,
		targetEnvVar:           *targetEnvName,
		terminationGracePeriod: *terminationGracePeriod,
		sigkillTimeout:         *sigkillTimeout,
		debouncePeriod:         *debouncePeriod,
	}
	if err := run(ctrl.SetupSignalHandler(), "ns-reloader", cfg); err != nil {
		log.Error(err, "Failed to run manager")
		os.Exit(1)
	}
}
