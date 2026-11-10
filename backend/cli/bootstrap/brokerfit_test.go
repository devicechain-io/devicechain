// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func brokerFitNode(cpu, memory string) corev1.Node {
	return corev1.Node{Status: corev1.NodeStatus{Allocatable: corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse(cpu),
		corev1.ResourceMemory: resource.MustParse(memory),
	}}}
}

func TestBrokerFitCapacity(t *testing.T) {
	three := func(cpu, memory string) []corev1.Node {
		return []corev1.Node{brokerFitNode(cpu, memory), brokerFitNode(cpu, memory), brokerFitNode(cpu, memory)}
	}
	cordoned := three("8", "16Gi")
	cordoned[2].Spec.Unschedulable = true
	tainted := three("8", "16Gi")
	tainted[2].Spec.Taints = []corev1.Taint{{Key: "dedicated", Effect: corev1.TaintEffectNoSchedule}}
	noExecute := three("8", "16Gi")
	noExecute[2].Spec.Taints = []corev1.Taint{{Key: "dedicated", Effect: corev1.TaintEffectNoExecute}}
	for _, tc := range []struct {
		name   string
		st     State
		nodes  []corev1.Node
		stated string
		want   []string
	}{
		{"cpu shortfall", State{HA: true, Size: SizePerf16k}, three("4", "16Gi"), "", []string{"perf-16k", "6 CPU", "6Gi memory", "0 of 3", "4 CPU", "16Gi", "allocatable check", "perf-12k asks 4 CPU / 4Gi"}},
		{"cpu shortfall on dry run", State{HA: true, Size: SizePerf16k, DryRun: true}, three("4", "16Gi"), "", []string{"perf-16k", "6 CPU"}},
		{"fits", State{HA: true, Size: SizePerf16k}, three("8", "16Gi"), "", nil},
		{"memory shortfall", State{HA: true, Size: SizePerf16k}, three("8", "4Gi"), "", []string{"perf-16k", "0 of 3", "6Gi memory"}},
		{"split resources", State{HA: true, Size: SizePerf16k}, []corev1.Node{brokerFitNode("8", "2Gi"), brokerFitNode("2", "16Gi"), brokerFitNode("2", "2Gi")}, "", []string{"0 of 3", "8 CPU", "16Gi"}},
		{"only two fit", State{HA: true, Size: SizePerf16k}, []corev1.Node{brokerFitNode("8", "16Gi"), brokerFitNode("8", "16Gi"), brokerFitNode("4", "16Gi")}, "", []string{"2 of 3"}},
		{"missing allocatable", State{HA: true, Size: SizePerf16k}, []corev1.Node{{}, {}, {}}, "", []string{"0 of 3"}},
		{"non HA", State{Size: SizePerf16k}, three("1", "1Gi"), "", nil},
		{"base", State{HA: true}, three("1", "1Gi"), "", nil},
		{"compact", State{HA: true, Compact: true, Size: SizePerf16k}, three("1", "1Gi"), "", nil},
		{"perf 12k", State{HA: true, Size: SizePerf12k}, three("4", "16Gi"), "", nil},
		{"operator CPU", State{HA: true, Size: SizePerf16k}, three("1", "1Gi"), "nats_cpu_request", nil},
		{"operator memory", State{HA: true, Size: SizePerf16k}, three("1", "1Gi"), "nats_memory_request", nil},
		{"cordoned", State{HA: true, Size: SizePerf16k}, cordoned, "", []string{"--ha places", "2 node(s)"}},
		{"NoSchedule", State{HA: true, Size: SizePerf16k}, tainted, "", []string{"--ha places", "2 node(s)"}},
		{"NoExecute", State{HA: true, Size: SizePerf16k}, noExecute, "", []string{"--ha places", "2 node(s)"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			originalList, originalStates := listNodes, operatorStatesVar
			t.Cleanup(func() { listNodes, operatorStatesVar = originalList, originalStates })
			listNodes = func(context.Context, string) ([]corev1.Node, error) { return tc.nodes, nil }
			operatorStatesVar = func(_ *State, name string) bool { return name == tc.stated }
			err := checkHaNodeCapacity(context.Background(), &tc.st)
			if len(tc.want) == 0 {
				if err != nil {
					t.Fatalf("unexpected refusal: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("accepted a cluster that cannot host the brokers")
			}
			for _, text := range tc.want {
				if !strings.Contains(err.Error(), text) {
					t.Errorf("error %q does not contain %q", err, text)
				}
			}
		})
	}
}

func TestBrokerFitListError(t *testing.T) {
	originalList, originalStates := listNodes, operatorStatesVar
	t.Cleanup(func() { listNodes, operatorStatesVar = originalList, originalStates })
	readErr := errors.New("cannot list nodes")
	listNodes = func(context.Context, string) ([]corev1.Node, error) { return nil, readErr }
	operatorStatesVar = func(*State, string) bool { return false }
	for _, dry := range []bool{true, false} {
		err := checkHaNodeCapacity(context.Background(), &State{HA: true, Size: SizePerf16k, DryRun: dry})
		if dry && err != nil {
			t.Errorf("dry run failed on a read error: %v", err)
		}
		if !dry && !errors.Is(err, readErr) {
			t.Errorf("real run did not preserve the read error: %v", err)
		}
	}
}

func TestBrokerFitRequests(t *testing.T) {
	for _, stated := range []string{"nats_cpu_request", "nats_memory_request", "both"} {
		req, unchecked, err := brokerRequestFor(&State{Size: SizePerf16k}, func(name string) bool {
			return name == stated || stated == "both"
		})
		if err != nil || req != nil || unchecked == "" {
			t.Fatalf("stated %s: got %v, %q, %v", stated, req, unchecked, err)
		}
		for _, name := range []string{"nats_cpu_request", "nats_memory_request"} {
			if (stated == name || stated == "both") && !strings.Contains(unchecked, name) {
				t.Errorf("unchecked reason %q omits %s", unchecked, name)
			}
		}
	}
	for _, field := range []string{"cpu", "memory"} {
		t.Run("invalid "+field, func(t *testing.T) {
			original := sizePresets[SizePerf16k]
			t.Cleanup(func() { sizePresets[SizePerf16k] = original })
			invalid := original
			if field == "cpu" {
				invalid.NatsCPURequest = "invalid"
			} else {
				invalid.NatsMemoryRequest = "invalid"
			}
			sizePresets[SizePerf16k] = invalid
			originalList, originalStates := listNodes, operatorStatesVar
			t.Cleanup(func() { listNodes, operatorStatesVar = originalList, originalStates })
			listNodes = func(context.Context, string) ([]corev1.Node, error) {
				t.Fatal("listed nodes before refusing invalid requests")
				return nil, nil
			}
			operatorStatesVar = func(*State, string) bool { return false }
			if err := checkHaNodeCapacity(context.Background(), &State{HA: true, Size: SizePerf16k, DryRun: true}); err == nil {
				t.Fatal("invalid preset request did not fail closed")
			}
		})
	}
}
