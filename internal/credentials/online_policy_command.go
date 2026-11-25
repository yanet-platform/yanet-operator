package credentials

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	api "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	"golang.org/x/sys/unix"
	"k8s.io/apimachinery/pkg/util/validation"
)

// RunPolicy delivers a projected policy or checks a local generation offline.
func RunPolicy(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("policy-sync", flag.ContinueOnError)
	source := flags.String("source", "/policy/bundle.json", "Projected policy bundle")
	destination := flags.String("destination", "/persist", "Node-local policy directory")
	epoch := flags.String("epoch", "", "Expected installation UID")
	node := flags.String("node-uid", "", "Expected Node UID")
	namespace := flags.String("namespace", "", "Expected installation namespace")
	domain := flags.String("domain", "", "Expected trust domain")
	role := flags.String("check-role", "", "Validate this role's mounted generation without the projection")
	checkProjection := flags.Bool("check-projection", false, "Check all projected roles against the installed generations")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *epoch == "" || *node == "" ||
		len(validation.IsDNS1123Label(*namespace)) != 0 ||
		len(validation.IsDNS1123Subdomain(*domain)) != 0 {
		return fmt.Errorf("policy-sync requires an installation UID, Node UID, namespace and trust domain")
	}
	scope := OnlinePolicyBundle{Epoch: *epoch, NodeUID: *node, Namespace: *namespace, Domain: *domain}
	if *role != "" {
		if *checkProjection || len(*role) > 40 || len(validation.IsDNS1123Label(*role)) != 0 {
			return fmt.Errorf("check-role requires a valid role and no projection check")
		}
		expected := policyScope{
			Epoch: scope.Epoch, NodeUID: scope.NodeUID,
			ServerURI: api.CertificateURI(scope.Domain, scope.Namespace, scope.Epoch, scope.NodeUID, *role),
		}
		return retryPublicationCheck(ctx, func() error { return checkOnlinePolicy(*destination, expected, "") })
	}
	if *checkProjection {
		return syncProjectedPolicy(*source, *destination, scope, nil)
	}

	// The HostPath mount root is created outside this mount namespace. Flush
	// its filesystem once before publication so its own ancestors are durable.
	root, err := os.Open(*destination) // #nosec G304 -- Mount path is configured by the delivery Pod.
	if err != nil {
		return err
	}
	syncErr := unix.Syncfs(int(root.Fd()))
	_ = root.Close()
	if syncErr != nil {
		return fmt.Errorf("confirm policy HostPath durability: %w", syncErr)
	}
	state := &policyDeliveryState{roles: map[string]*policyDeliveryRole{}}
	defer state.close()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	var previousError string
	for ctx.Err() == nil {
		err := syncProjectedPolicy(*source, *destination, scope, state)
		if err == nil {
			previousError = ""
		} else if message := err.Error(); message != previousError {
			slog.Error("gateway policy delivery failed", "error", err)
			previousError = message
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
	return nil
}

type policyDeliveryRole struct {
	durable    durableCurrent
	lastLogged string
}

type policyDeliveryState struct {
	projection  []byte
	generations []onlinePolicyGeneration
	roles       map[string]*policyDeliveryRole
}

func (s *policyDeliveryState) close() {
	for _, role := range s.roles {
		role.durable.forget()
	}
}

func readProjectedPolicy(source string, expected OnlinePolicyBundle, state *policyDeliveryState) ([]onlinePolicyGeneration, error) {
	file, err := os.Open(source) // #nosec G304 -- The file path is provided by the delivery workload.
	if err != nil {
		return nil, err
	}
	defer file.Close()
	value, err := io.ReadAll(io.LimitReader(file, MaxPolicyBundleSize+1))
	if err != nil {
		return nil, err
	}
	if state != nil && len(state.projection) != 0 && bytes.Equal(value, state.projection) {
		return state.generations, nil
	}
	bundle, err := DecodeOnlinePolicyBundle(value)
	if err != nil {
		return nil, err
	}
	if bundle.Epoch != expected.Epoch || bundle.NodeUID != expected.NodeUID ||
		bundle.Namespace != expected.Namespace || bundle.Domain != expected.Domain {
		return nil, fmt.Errorf("projected policy is outside the configured installation and Node scope")
	}
	files, err := bundleFiles(bundle)
	if err != nil {
		return nil, err
	}
	generations, err := onlinePolicyGenerations(bundle, files)
	if err != nil {
		return nil, err
	}
	if state != nil {
		wanted := make(map[string]bool, len(generations))
		for _, generation := range generations {
			wanted[generation.role] = true
		}
		for role, publication := range state.roles {
			if !wanted[role] {
				publication.durable.forget()
				delete(state.roles, role)
			}
		}
		state.projection, state.generations = value, generations
	}
	return generations, nil
}

// A nil delivery state checks the projection without publishing.
func syncProjectedPolicy(source, destination string, expected OnlinePolicyBundle, state *policyDeliveryState) error {
	generations, err := readProjectedPolicy(source, expected, state)
	if err != nil {
		return err
	}
	var failures []error
	for _, generation := range generations {
		path := filepath.Join(destination, generation.role)
		var roleErr error
		if state == nil {
			roleErr = checkOnlinePolicy(path, generation.manifest.Scope, generation.name)
		} else {
			roleErr = state.publish(path, generation)
		}
		if roleErr != nil {
			failures = append(failures, fmt.Errorf("server role %s: %w", generation.role, roleErr))
		}
	}
	return errors.Join(failures...)
}

func (s *policyDeliveryState) publish(path string, generation onlinePolicyGeneration) error {
	role := s.roles[generation.role]
	if role == nil {
		role = &policyDeliveryRole{}
		s.roles[generation.role] = role
	}
	committed, err := publishOnlinePolicy(path, generation, &role.durable)
	if committed && role.lastLogged != generation.name {
		role.lastLogged = generation.name
		scope := generation.manifest.Scope
		slog.Info("gateway policy published", "epoch", scope.Epoch, "node_uid", scope.NodeUID,
			"server_uri", scope.ServerURI, "sequence", generation.manifest.Sequence, "generation", generation.name)
	}
	return err
}
