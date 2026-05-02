package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/google/shlex"
)

// Config is the validated runtime configuration parsed from env vars.
type Config struct {
	Mode        string   // "persistent-process" | "per-prompt-process"
	DataSocket  string   // PADDOCK_AGENT_DATA_SOCKET
	CtlSocket   string   // PADDOCK_AGENT_CTL_SOCKET
	HarnessBin  string   // absolute path to harness CLI
	HarnessArgs []string // argv tail for the CLI
	WorkDir     string   // optional cwd; defaults to $PADDOCK_WORKSPACE
}

// validateEnv parses the env map into a Config or returns an error
// naming the offending var. Caller passes a map (rather than reading
// os.Getenv directly) so tests can inject without t.Setenv churn.
func validateEnv(env map[string]string) (Config, error) {
	mode := env["PADDOCK_INTERACTIVE_MODE"]
	switch mode {
	case "":
		return Config{}, fmt.Errorf("PADDOCK_INTERACTIVE_MODE is required")
	case "persistent-process", "per-prompt-process":
	default:
		return Config{}, fmt.Errorf("PADDOCK_INTERACTIVE_MODE must be persistent-process or per-prompt-process, got %q", mode)
	}

	required := []string{"PADDOCK_AGENT_DATA_SOCKET", "PADDOCK_AGENT_CTL_SOCKET", "PADDOCK_HARNESS_BIN"}
	for _, k := range required {
		if env[k] == "" {
			return Config{}, fmt.Errorf("%s is required", k)
		}
	}

	args, err := shlex.Split(env["PADDOCK_HARNESS_ARGS"])
	if err != nil {
		return Config{}, fmt.Errorf("PADDOCK_HARNESS_ARGS: %w", err)
	}

	workdir := env["PADDOCK_HARNESS_WORKDIR"]
	if workdir == "" {
		workdir = env["PADDOCK_WORKSPACE"]
	}

	return Config{
		Mode:        mode,
		DataSocket:  env["PADDOCK_AGENT_DATA_SOCKET"],
		CtlSocket:   env["PADDOCK_AGENT_CTL_SOCKET"],
		HarnessBin:  env["PADDOCK_HARNESS_BIN"],
		HarnessArgs: args,
		WorkDir:     workdir,
	}, nil
}

func main() {
	logger := log.New(os.Stderr, "paddock-harness-supervisor: ", log.LstdFlags)

	cfg, err := validateEnv(envMap())
	if err != nil {
		logger.Fatalf("invalid env: %v", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	if err := run(ctx, logger, cfg); err != nil {
		logger.Fatalf("run: %v", err)
	}
}

func envMap() map[string]string {
	out := make(map[string]string)
	for _, kv := range os.Environ() {
		if i := strings.IndexByte(kv, '='); i > 0 {
			out[kv[:i]] = kv[i+1:]
		}
	}
	return out
}

// run is the top-level dispatch. Stub for now; real impl in Tasks 2 & 3.
func run(ctx context.Context, logger *log.Logger, cfg Config) error {
	switch cfg.Mode {
	case "persistent-process":
		return runPersistent(ctx, logger, cfg)
	case "per-prompt-process":
		return runPerPrompt(ctx, logger, cfg)
	}
	return fmt.Errorf("unreachable: validateEnv accepted invalid mode %q", cfg.Mode)
}

// Stubs so the file compiles before Tasks 2 & 3 land.
func runPersistent(ctx context.Context, logger *log.Logger, cfg Config) error {
	return fmt.Errorf("persistent-process mode not yet implemented")
}
func runPerPrompt(ctx context.Context, logger *log.Logger, cfg Config) error {
	return fmt.Errorf("per-prompt-process mode not yet implemented")
}
