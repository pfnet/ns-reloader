package main

import (
	"fmt"
	"os"
	"time"

	"go.uber.org/zap/zapcore"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

var (
	// version and commit are set via ldflags during build
	version = "dev"
	commit  = "unknown"
)

const (
	// EnvKey_WatchNamespaceSelector is the environment variable key to
	// specify the label selector for namespaces to watch by reloader.
	EnvKey_WatchNamespaceSelector = "RELOADER_NAMESPACE_SELECTOR"
	// EnvKey_TargetEnvKey specifies the key name of the environment variable
	// where the list of watched namespaces will be set for the wrapped process.
	EnvKey_TargetEnvKey = "RELOADER_TARGET_ENV_KEY"

	processTerminationGracePeriod = 5 * time.Second
	processDebouncePeriod         = 5 * time.Second
)

func main() {
	log := zap.New(zap.UseFlagOptions(&zap.Options{
		Development: true,
		TimeEncoder: zapcore.RFC3339TimeEncoder,
		Level:       zapcore.InfoLevel,
	})).WithName("reloader")
	ctrl.SetLogger(log)
	log.Info("Starting ns-reloader", "version", version, "commit", commit)

	// Find the separator "--" to split reloader flags from wrapped command
	var cmds []string
	for i, arg := range os.Args {
		if arg == "--" {
			if i+1 >= len(os.Args) {
				log.Error(fmt.Errorf("no command specified after --"), "Usage: reloader [flags] -- <command> [args...]")
				os.Exit(1)
			}
			cmds = os.Args[i+1:]
			break
		}
	}
	if len(cmds) == 0 {
		log.Error(fmt.Errorf("missing -- separator"), "Usage: reloader [flags] -- <command> [args...]")
		os.Exit(1)
	}

	if err := run(ctrl.SetupSignalHandler(), log, cmds, nil); err != nil {
		log.Error(err, "Failed to run manager")
		os.Exit(1)
	}
}
