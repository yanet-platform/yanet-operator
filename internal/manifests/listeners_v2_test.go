/*
Copyright 2023-2026 YANDEX LLC.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package manifests

import (
	"strings"
	"testing"

	"github.com/yanet-platform/yanet-operator/internal/helpers"
	corev1 "k8s.io/api/core/v1"
)

func TestConfigureListeners_SidecarReservedTarget(t *testing.T) {
	sidecar := &helpers.ResolvedComponent{Kind: helpers.KindSidecar, Name: "worker", Image: helpers.ResolvedImage{Name: "worker"}}
	target := BuildServices(ctxV2(), sidecar)[0].Ports[0].TargetPortName
	for _, tt := range []struct {
		name     string
		kind     helpers.ComponentKind
		netlink  bool
		init     bool
		portName string
		wantErr  bool
	}{
		{name: "enabled sidecar", kind: helpers.KindDataplane, netlink: true, portName: target, wantErr: true},
		{name: "disabled sidecar", kind: helpers.KindDataplane, portName: target, wantErr: true},
		{name: "disabled sidecar and another native sidecar", kind: helpers.KindDataplane, init: true, portName: target, wantErr: true},
		{name: "unrelated port", kind: helpers.KindDataplane, portName: "custom"},
		{name: "unrelated workload", kind: helpers.KindOperator, portName: target},
	} {
		t.Run(tt.name, func(t *testing.T) {
			component := &helpers.ResolvedComponent{
				Kind: tt.kind, Name: string(tt.kind), Enabled: true,
				Image:      helpers.ResolvedImage{Name: "dataplane"},
				Containers: []helpers.ResolvedContainer{{Name: "operator", Image: helpers.ResolvedImage{Name: "operator"}}},
			}
			if tt.kind == helpers.KindDataplane {
				resolved := *sidecar
				resolved.Enabled = tt.netlink
				component.Sidecars = []*helpers.ResolvedComponent{&resolved}
			}
			deployments, err := RenderDeployments(ctxV2(), component, nil)
			if err != nil {
				t.Fatal(err)
			}
			pod := &deployments[0].Spec.Template.Spec
			other := corev1.Container{Name: "other", Image: "other", Ports: []corev1.ContainerPort{{
				Name: tt.portName, ContainerPort: 9000,
			}}}
			if tt.init {
				always := corev1.ContainerRestartPolicyAlways
				other.RestartPolicy = &always
				pod.InitContainers = append(pod.InitContainers, other)
			} else {
				pod.Containers = append(pod.Containers, other)
			}
			err = ConfigureListeners(deployments[0], component)
			if tt.wantErr {
				if err == nil || !strings.Contains(err.Error(), target) || !strings.Contains(err.Error(), "other") {
					t.Fatalf("reserved target name must not be captured: %v", err)
				}
			} else if err != nil {
				t.Fatalf("unrelated port rejected: %v", err)
			}
		})
	}
}
