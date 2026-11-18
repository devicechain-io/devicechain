// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"strings"
	"testing"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
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
		{"perf 20k shortfall", State{HA: true, Size: SizePerf20k}, three("4", "16Gi"), "", []string{"perf-20k", "6 CPU", "6Gi memory", "0 of 3"}},
		{"perf 20k fits", State{HA: true, Size: SizePerf20k}, three("8", "16Gi"), "", nil},
		{"fits", State{HA: true, Size: SizePerf16k}, three("8", "16Gi"), "", nil},
		{"memory shortfall", State{HA: true, Size: SizePerf16k}, three("8", "4Gi"), "", []string{"perf-16k", "0 of 3", "6Gi memory"}},
		{"split resources", State{HA: true, Size: SizePerf16k}, []corev1.Node{brokerFitNode("8", "2Gi"), brokerFitNode("2", "16Gi"), brokerFitNode("2", "2Gi")}, "", []string{"0 of 3", "8 CPU", "16Gi"}},
		{"only two fit", State{HA: true, Size: SizePerf16k}, []corev1.Node{brokerFitNode("8", "16Gi"), brokerFitNode("8", "16Gi"), brokerFitNode("4", "16Gi")}, "", []string{"2 of 3"}},
		{"missing allocatable", State{HA: true, Size: SizePerf16k}, []corev1.Node{{}, {}, {}}, "", []string{"0 of 3"}},
		{"non HA", State{Size: SizePerf16k}, three("1", "1Gi"), "", nil},
		{"base", State{HA: true}, three("1", "1Gi"), "", nil},
		{"compact", State{HA: true, Compact: true, Size: SizePerf16k}, three("1", "1Gi"), "", nil},
		{"perf 12k", State{HA: true, Size: SizePerf12k}, three("4", "16Gi"), "", nil},
		{"perf 12k shortfall", State{HA: true, Size: SizePerf12k}, three("2", "16Gi"), "", []string{"4 CPU", "base asks for less"}},
		{"operator CPU", State{HA: true, Size: SizePerf16k}, three("8", "4Gi"), "nats_cpu_request", []string{"0 of 3", "6Gi memory"}},
		{"operator memory", State{HA: true, Size: SizePerf16k}, three("4", "16Gi"), "nats_memory_request", []string{"0 of 3", "6 CPU"}},
		{"operator both", State{HA: true, Size: SizePerf16k}, three("1", "1Gi"), "both", nil},
		{"cordoned", State{HA: true, Size: SizePerf16k}, cordoned, "", []string{"--ha places", "2 node(s)"}},
		{"NoSchedule", State{HA: true, Size: SizePerf16k}, tainted, "", []string{"--ha places", "2 node(s)"}},
		{"NoExecute", State{HA: true, Size: SizePerf16k}, noExecute, "", []string{"--ha places", "2 node(s)"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			originalList, originalStates := listNodes, operatorStatesVar
			t.Cleanup(func() { listNodes, operatorStatesVar = originalList, originalStates })
			listNodes = func(context.Context, string) ([]corev1.Node, error) { return tc.nodes, nil }
			operatorStatesVar = func(_ *State, name string) bool { return name == tc.stated || tc.stated == "both" }
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
			var absent []string
			if tc.st.Size == SizePerf12k {
				absent = append(absent, "perf-12k asks")
			}
			if tc.stated == "nats_cpu_request" {
				absent = append(absent, "CPU", "nats_cpu_request")
			}
			if tc.stated == "nats_memory_request" {
				absent = append(absent, "memory", "nats_memory_request", "6Gi")
			}
			for _, text := range absent {
				if strings.Contains(err.Error(), text) {
					t.Errorf("error %q unexpectedly contains %q", err, text)
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
	t.Parallel()
	for _, stated := range []string{"nats_cpu_request", "nats_memory_request", "both"} {
		req, unchecked, err := brokerRequestFor(&State{Size: SizePerf16k}, func(name string) bool {
			return name == stated || stated == "both"
		})
		if err != nil || unchecked == "" {
			t.Fatalf("stated %s: got %v, %q, %v", stated, req, unchecked, err)
		}
		if stated == "both" {
			if req != nil {
				t.Fatalf("both stated: got a checked request: %v", req)
			}
		} else {
			if req == nil {
				t.Fatalf("stated %s: omitted the remaining preset request", stated)
			}
			if (req.CPU == nil) != (stated == "nats_cpu_request") || (req.Memory == nil) != (stated == "nats_memory_request") {
				t.Errorf("stated %s: incorrect checked resources: %+v", stated, req)
			}
		}
		for _, name := range []string{"nats_cpu_request", "nats_memory_request"} {
			if strings.Contains(unchecked, name) != (stated == name || stated == "both") {
				t.Errorf("stated %s: unchecked reason %q incorrectly reports %s", stated, unchecked, name)
			}
		}
	}
	for _, field := range []string{"cpu", "memory"} {
		t.Run("invalid "+field, func(t *testing.T) {
			t.Parallel()
			st := &State{HA: true, Size: SizePerf16k, DryRun: true}
			invalid := *sizeFor(st)
			if field == "cpu" {
				invalid.NatsCPURequest = "invalid"
			} else {
				invalid.NatsMemoryRequest = "invalid"
			}
			// Invalid fixtures must not affect concurrent readers of the built-in preset.
			stated := func(string) bool { return false }
			req, _, err := brokerRequestFor(st, stated)
			if err != nil || req == nil || req.CPUText != "6" || req.MemText != "6Gi" {
				t.Fatalf("invalid fixture changed the built-in requests: %+v, %v", req, err)
			}
			_, _, err = invalid.brokerRequestFor(st, stated)
			want := "parsing " + SizePerf16k + " nats_" + field + "_request:"
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("invalid preset request: got %v, want error containing %q", err, want)
			}
		})
	}
}

func TestDeclareInstanceChecksBrokerFitBeforeWriting(t *testing.T) {
	originalRead, originalWrite := readInstanceDeclaration, writeInstanceDeclaration
	originalList, originalStates := listNodes, operatorStatesVar
	t.Cleanup(func() {
		readInstanceDeclaration, writeInstanceDeclaration = originalRead, originalWrite
		listNodes, operatorStatesVar = originalList, originalStates
	})
	readInstanceDeclaration = func(context.Context, string, string) (*dcv1beta1.Instance, error) {
		return nil, nil
	}
	writes := 0
	writeInstanceDeclaration = func(context.Context, string, string, dcv1beta1.InstanceSpec, string, bool) error {
		writes++
		return nil
	}
	listNodes = func(context.Context, string) ([]corev1.Node, error) {
		return []corev1.Node{brokerFitNode("4", "16Gi"), brokerFitNode("4", "16Gi"), brokerFitNode("4", "16Gi")}, nil
	}
	operatorStatesVar = func(*State, string) bool { return false }

	st := &State{Instance: "x", HA: true, Size: SizePerf16k, Provider: "local", Profile: "default"}
	err := stepDeclareInstance(context.Background(), st)
	if err == nil || !strings.Contains(err.Error(), "perf-16k") {
		t.Fatalf("expected perf-16k node-fit refusal, got %v", err)
	}
	if writes != 0 {
		t.Fatalf("wrote %d declarations before refusing node fit", writes)
	}
	message := err.Error()
	fileAdvice := strings.Index(message, "terraform.tfvars")
	for _, name := range []string{"TF_VAR_nats_cpu_request", "TF_VAR_nats_memory_request"} {
		if at := strings.Index(message, name); at < 0 || fileAdvice < 0 || at >= fileAdvice {
			t.Errorf("expected %s advice before terraform.tfvars: %s", name, message)
		}
	}

	st = &State{Instance: "x", HA: true, Size: SizePerf12k, Provider: "local", Profile: "default"}
	err = stepDeclareInstance(context.Background(), st)
	if err != nil && strings.Contains(err.Error(), "allocatable") {
		t.Fatalf("perf-12k was refused by node fit: %v", err)
	}
	if writes != 1 {
		t.Fatalf("perf-12k did not reach the declaration write (writes=%d): %v", writes, err)
	}
}
