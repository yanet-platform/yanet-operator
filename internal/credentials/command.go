package credentials

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"time"
)

type dnsNames []string

func (d *dnsNames) String() string         { return fmt.Sprint([]string(*d)) }
func (d *dnsNames) Set(value string) error { *d = append(*d, value); return nil }

// Run starts the node-local exporter or checks the last published generation.
// The exporter has no API credentials; kubelet projects precisely its Secrets.
func Run(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("credential-sync", flag.ContinueOnError)
	source := flags.String("source", "/source", "Kubelet Secret projection directory")
	destination := flags.String("destination", "/persist", "Persistent generation directory")
	trust := flags.String("trust", "/trust/ca.crt", "Independent public trust bundle")
	uri := flags.String("uri", "", "Exact expected URI SAN")
	usage := flags.String("usage", "client", "Expected client or server EKU")
	check := flags.Bool("check", false, "Validate the installed generation and exit")
	var names dnsNames
	flags.Var(&names, "dns", "Exact expected DNS SAN, repeatable")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *uri == "" || flags.NArg() != 0 {
		return fmt.Errorf("an expected URI and no positional arguments are required")
	}
	identity := Identity{URI: *uri, Usage: *usage, DNSNames: names}
	if *check {
		return Check(*destination, *trust, identity, time.Now())
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	lastGeneration := ""
	for {
		generation, err := Publish(*source, *destination, *trust, identity, time.Now())
		if err != nil {
			slog.Error("credential delivery failed; keeping the previous generation", "error", err)
		} else if generation != lastGeneration {
			slog.Info("credential generation delivered", "generation", generation)
			lastGeneration = generation
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
