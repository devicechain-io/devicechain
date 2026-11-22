// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	corev1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
	"github.com/devicechain-io/dc-k8s/controllers"
	"github.com/devicechain-io/dc-k8s/executor"
	"github.com/devicechain-io/dc-k8s/internal/manageropts"
	//+kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(corev1beta1.AddToScheme(scheme))
	//+kubebuilder:scaffold:scheme
}

func main() {
	managerOptions := manageropts.BindManagerFlags(flag.CommandLine)
	executorMode := flag.Bool("executor", false, "Run the Preview executor controllers.")
	managerOptions.Scheme = scheme
	managerOptions.Cache = controllers.CacheOptions()
	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	if *executorMode {
		managerOptions.LeaderElectionID = "dc-executor"
		managerOptions.LeaderElectionNamespace = os.Getenv("POD_NAMESPACE")
		if managerOptions.LeaderElectionNamespace == "" {
			setupLog.Error(fmt.Errorf("POD_NAMESPACE is required"), "executor namespace unavailable")
			os.Exit(1)
		}
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), *managerOptions)
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	if err := registerComponents(*executorMode, managerRegistrar{mgr}); err != nil {
		setupLog.Error(err, "unable to register manager components")
		os.Exit(1)
	}
	//+kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

// registrar is the startup boundary for controllers and manager runnables.
type registrar interface {
	Register(name string) error
}

func registerComponents(mode bool, r registrar) error {
	for _, name := range manageropts.ControllersFor(mode) {
		if err := r.Register(name); err != nil {
			return fmt.Errorf("registering %s: %w", name, err)
		}
	}
	return nil
}

type managerRegistrar struct{ manager ctrl.Manager }

func (r managerRegistrar) Register(name string) error {
	mgr := r.manager
	switch name {
	case "executor":
		return executor.SetupExecutor(mgr, executor.Options{})
	case "instance":
		return (&controllers.InstanceReconciler{
			Client: mgr.GetClient(),
			Scheme: mgr.GetScheme(),
		}).SetupWithManager(mgr)
	case "snapshot-retention":
		// Snapshot pruning is a timer runnable, not an informer.
		return mgr.Add(&controllers.SnapshotRetention{
			Reader: mgr.GetAPIReader(), Writer: mgr.GetClient(),
			Recorder: mgr.GetEventRecorder("snapshot-retention"),
			Interval: 10 * time.Minute, Now: time.Now,
		})
	default:
		return fmt.Errorf("unknown controller %q", name)
	}
}
