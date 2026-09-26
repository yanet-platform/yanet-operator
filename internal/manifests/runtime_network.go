package manifests

import (
	"encoding/json"
	"fmt"

	api "github.com/yanet-platform/yanet-operator/api/v1alpha1"
	"github.com/yanet-platform/yanet-operator/internal/helpers"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

// GatewayEndpointOverride matches the shared runtime's named selection contract.
type GatewayEndpointOverride struct {
	Name     string `json:"name"`
	Endpoint string `json:"endpoint"`
}

// WithRuntimeNetwork resolves physical NUMA endpoints once for a rendering pass.
func WithRuntimeNetwork(ctx BuildContext, config *api.YanetConfigSpec, spec *api.YanetSpec) (BuildContext, error) {
	controlplane, err := helpers.ResolveBoxComponent(config, spec, helpers.KindControlplane, "")
	if err != nil {
		return ctx, err
	}
	if controlplane != nil {
		ctx.Gateways = []GatewayEndpointOverride{}
		disabled := disabledNumaSet(controlplane)
		for index := int32(0); index < effectiveNuma(controlplane); index++ {
			if _, skip := disabled[index]; skip {
				continue
			}
			ctx.Gateways = append(ctx.Gateways, GatewayEndpointOverride{
				Name:     fmt.Sprintf("numa%d", index),
				Endpoint: serviceEndpoint(ctx, SharedServiceName(ctx.BoxType, controlplane.Name, &index), ServiceGRPCPort),
			})
		}
	}
	return ctx, nil
}

// ConfigureRuntimeNetwork injects runtime endpoints only for managed host configs.
// Configuration data remains opaque; unrelated host volumes do not enable overrides.
func ConfigureRuntimeNetwork(deployment *appsv1.Deployment, ctx BuildContext, component *helpers.ResolvedComponent) error {
	if component.Kind != helpers.KindDataplane {
		if err := configureRuntimeContainer(deployment, ctx, component, false); err != nil {
			return err
		}
	}
	for _, sidecar := range component.Sidecars {
		if sidecar.Enabled {
			if err := configureRuntimeContainer(deployment, ctx, sidecar, true); err != nil {
				return err
			}
		}
	}
	return nil
}

func configureRuntimeContainer(deployment *appsv1.Deployment, ctx BuildContext, component *helpers.ResolvedComponent, composed bool) error {
	name := ListenerContainerName(component)
	configName := "config"
	source := component.Config
	if component.Kind == helpers.KindOperator {
		if len(component.Containers) == 0 {
			return fmt.Errorf("operator %q has no primary container", component.Name)
		}
		configName = "config-0"
		source = component.Containers[0].Config
	}
	if source.IsZero() {
		return nil
	}
	if composed {
		name = scopedOperatorName(component.Name, name)
		configName = scopedOperatorName(component.Name, configName)
	}
	container, err := findContainer(deployment, name)
	if err != nil {
		return err
	}
	if !managedHostConfig(&deployment.Spec.Template.Spec, container, configName) {
		return nil
	}
	grpcPort, httpPort, err := runtimePortPair(component)
	if err != nil {
		return err
	}
	bind := func(port int32) string { return fmt.Sprintf("[::]:%d", port) }
	var variables []corev1.EnvVar
	switch component.Kind {
	case helpers.KindControlplane:
		variables = []corev1.EnvVar{
			{Name: "YANET_GATEWAY_SERVER_ENDPOINT", Value: bind(grpcPort)},
			{Name: "YANET_GATEWAY_SERVER_HTTP_ENDPOINT", Value: bind(httpPort)},
		}
	case helpers.KindBirdAdapter:
		variables = []corev1.EnvVar{
			{Name: "YANET_LISTEN_ADDR", Value: bind(grpcPort)},
			{Name: "YANET_ROUTE_OPERATOR_ENDPOINT", Value: serviceEndpoint(ctx, SharedServiceName(ctx.BoxType, "route", nil), ServiceGRPCPort)},
		}
	default:
		endpoint := grpcPort
		if len(component.ListenerNames) == 1 && component.ListenerNames[0] == ListenerHTTP {
			endpoint = httpPort
		}
		variables = []corev1.EnvVar{{Name: "YANET_SERVER_ENDPOINT", Value: bind(endpoint)}}
		for _, listener := range ListenerPorts(component) {
			if listener.Name == ListenerGRPC {
				variables = append(variables, corev1.EnvVar{
					Name:  "YANET_SERVER_ADVERTISE_ENDPOINT",
					Value: serviceEndpoint(ctx, SharedServiceName(ctx.BoxType, component.Name, nil), ServiceGRPCPort),
				})
			}
		}
		if ctx.Gateways != nil {
			value, err := json.Marshal(ctx.Gateways)
			if err != nil {
				return fmt.Errorf("encode gateway endpoints: %w", err)
			}
			variables = append(variables, corev1.EnvVar{Name: "YANET_KUBERNETES_GATEWAYS", Value: string(value)})
		}
	}
	owned := make(map[string]bool, len(variables))
	for _, variable := range variables {
		owned[variable.Name] = true
	}
	for _, variable := range container.Env {
		if !owned[variable.Name] {
			variables = append(variables, variable)
		}
	}
	container.Env = variables
	return nil
}

func managedHostConfig(pod *corev1.PodSpec, container *corev1.Container, name string) bool {
	mounted := false
	for _, mount := range container.VolumeMounts {
		if mount.Name == name {
			mounted = true
		}
	}
	if !mounted {
		return false
	}
	for _, volume := range pod.Volumes {
		if volume.Name == name {
			return volume.HostPath != nil
		}
	}
	return false
}

func serviceEndpoint(ctx BuildContext, name string, port int32) string {
	return fmt.Sprintf("%s.%s.svc.cluster.local:%d", name, ctx.Namespace, port)
}
