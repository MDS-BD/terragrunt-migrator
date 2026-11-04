package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/MDS-BD/terragrunt-migrator/internal/cli"
	"github.com/MDS-BD/terragrunt-migrator/internal/pkg/config"
	"github.com/mattn/go-isatty"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/pkgerrors"
)

var version = "dev"

func main() {
	zerolog.ErrorStackMarshaler = pkgerrors.MarshalStack
	zerolog.MessageFieldName = "msg"
	if err := config.Load(); err != nil {
		fmt.Fprintf(os.Stderr, "invalid configuration: %v\n", err)
		os.Exit(1)
	}
	var output io.Writer
	if config.Cfg.LogFormat == "json" {
		output = os.Stderr
	} else {
		output = zerolog.ConsoleWriter{
			Out:           os.Stderr,
			NoColor:       os.Getenv("NO_COLOR") != "" || !isatty.IsTerminal(os.Stderr.Fd()),
			PartsOrder:    []string{zerolog.TimestampFieldName, zerolog.LevelFieldName, zerolog.MessageFieldName},
			FieldsExclude: []string{"tfStage", "tfComponent", "oldLine", "newLine", "stack", "command", "file", "module"},
			FormatExtra:   formatConsoleDetails,
		}
	}
	logger := zerolog.New(output).Level(config.Cfg.LogLevel).With().Timestamp().Logger()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx = logger.WithContext(ctx)
	root := cli.NewRoot(version)
	root.SetArgs(commandArgs(os.Args[1:]))
	err := root.ExecuteContext(ctx)
	if errors.Is(err, context.Canceled) || ctx.Err() != nil {
		zerolog.Ctx(ctx).Warn().Msg("Interrupted; shutdown complete")
		os.Exit(130)
	}
	if err != nil {
		zerolog.Ctx(ctx).Error().Stack().Err(err).Msg("terragrunt-migrator failed")
		os.Exit(1)
	}
}

func formatConsoleDetails(fields map[string]any, out *bytes.Buffer) error {
	if file, ok := fields["file"].(string); ok && file != "" {
		fmt.Fprintf(out, "\n  File: %s", file)
	}
	if module, ok := fields["module"].(string); ok && module != "" {
		fmt.Fprintf(out, "\n  Module: %s", module)
	}
	if command, ok := fields["command"].(string); ok && command != "" {
		fmt.Fprintf(out, "\n  $ %s", command)
	}
	if oldLine, ok := fields["oldLine"].(string); ok && oldLine != "" {
		fmt.Fprintf(out, "\n  - %s", oldLine)
	}
	if newLine, ok := fields["newLine"].(string); ok && newLine != "" {
		fmt.Fprintf(out, "\n  + %s", newLine)
	}
	if stack, ok := fields["stack"].([]any); ok {
		for _, frame := range stack {
			if details, ok := frame.(map[string]any); ok {
				fmt.Fprintf(out, "\n    %s:%s %s", details["source"], details["line"], details["func"])
			}
		}
	}
	return nil
}

// Yargs accepts both --flag=false and --flag false; pflag only accepts the former.
func commandArgs(args []string) []string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--" {
			break
		}
		switch args[i] {
		case "--dry-run", "--auto-approve", "--auto-apply":
			if args[i+1] == "true" || args[i+1] == "false" {
				args[i] += "=" + args[i+1]
				copy(args[i+1:], args[i+2:])
				args = args[:len(args)-1]
			}
		}
	}
	return args
}
