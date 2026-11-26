package credentials

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"time"
)

// RunPolicy copies projected policy to persistent files or checks local delivery.
func RunPolicy(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("policy-sync", flag.ContinueOnError)
	source := flags.String("source", "/policy/bundle.json", "Projected public policy")
	destination := flags.String("destination", "/persist", "Node-local policy directory")
	check := flags.Bool("check", false, "Check local account files without the projection")
	checkProjection := flags.Bool("check-projection", false, "Compare local files with the projection")
	once := flags.Bool("once", false, "Publish once and exit")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *source == "" || *destination == "" ||
		(*check && *checkProjection) || (*once && (*check || *checkProjection)) {
		return fmt.Errorf("policy-sync requires paths and at most one of --check, --check-projection, --once")
	}
	if *check {
		return checkPolicyFiles(*destination, nil)
	}
	sync := func() error {
		files, err := readProjectedPolicy(*source)
		if err != nil {
			return err
		}
		if *checkProjection {
			return checkPolicyFiles(*destination, files)
		}
		changed, err := publishPolicyFiles(*destination, files)
		if err == nil && changed {
			slog.Info("gateway policy files delivered")
		}
		return err
	}
	if *once || *checkProjection {
		return sync()
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	var previousError string
	for ctx.Err() == nil {
		if err := sync(); err != nil {
			if message := err.Error(); message != previousError {
				slog.Error("gateway policy delivery failed", "error", err)
				previousError = message
			}
		} else if previousError != "" {
			slog.Info("gateway policy delivery recovered")
			previousError = ""
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
	return nil
}
