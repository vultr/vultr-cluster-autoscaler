/*
Copyright 2016 The Kubernetes Authors.

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

package main

import (
	"context"
	"flag"
	"net/http"
	"os"
	"time"

	"github.com/spf13/pflag"
	_ "github.com/vultr/vultr-cluster-autoscaler/cloudprovider/vultr"
	"github.com/vultr/vultr-cluster-autoscaler/version"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apiserver/pkg/server/mux"
	"k8s.io/apiserver/pkg/server/routes"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	cqv1beta1 "k8s.io/autoscaler/cluster-autoscaler/apis/capacityquota/autoscaling.x-k8s.io/v1beta1"
	"k8s.io/client-go/informers"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	kube_flag "k8s.io/component-base/cli/flag"
	componentbaseconfig "k8s.io/component-base/config"
	componentopts "k8s.io/component-base/config/options"
	"k8s.io/component-base/logs"
	logsapi "k8s.io/component-base/logs/api/v1"
	_ "k8s.io/component-base/logs/json/register"
	"k8s.io/component-base/metrics/legacyregistry"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/features"
	autoscalerbuilder "sigs.k8s.io/cluster-autoscaler/pkg/builder"
	"sigs.k8s.io/cluster-autoscaler/pkg/config"
	"sigs.k8s.io/cluster-autoscaler/pkg/config/flags"
	"sigs.k8s.io/cluster-autoscaler/pkg/core"
	"sigs.k8s.io/cluster-autoscaler/pkg/debuggingsnapshot"
	"sigs.k8s.io/cluster-autoscaler/pkg/loop"
	"sigs.k8s.io/cluster-autoscaler/pkg/metrics"
	kube_util "sigs.k8s.io/cluster-autoscaler/pkg/utils/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

const (
	defaultLeaseDuration = 15 * time.Second
	defaultRenewDeadline = 10 * time.Second
	defaultRetryPeriod   = 2 * time.Second
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(cqv1beta1.AddToScheme(scheme))
}

func run(ctx context.Context, healthCheck *metrics.HealthCheck, debuggingSnapshotter debuggingsnapshot.DebuggingSnapshotter, opts config.AutoscalingOptions) {
	metrics.RegisterAll(opts.EmitPerNodeGroupMetrics)

	restConfig := kube_util.GetKubeConfig(opts.KubeClientOpts)
	mgr, err := ctrl.NewManager(restConfig, ctrl.Options{
		Scheme:                  scheme,
		Cache:                   cache.Options{DefaultTransform: cache.TransformStripManagedFields()},
		LeaderElection:          false,
		Metrics:                 metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress:  "0",
		PprofBindAddress:        "0",
		GracefulShutdownTimeout: new(time.Duration(-1)),
	})
	if err != nil {
		klog.Fatalf("Failed to create manager: %v", err)
	}

	autoscaler, trigger := mustBuildAutoscaler(ctx, opts, debuggingSnapshotter, mgr)
	healthCheck.StartMonitoring()
	if err := autoscaler.Start(); err != nil {
		klog.Fatalf("Failed to start autoscaler background components: %v", err)
	}

	if err := mgr.Add(manager.RunnableFunc(func(ctx context.Context) error {
		defer autoscaler.ExitCleanUp()
		iteration := 0
		if opts.FrequentLoopsEnabled {
			lastRun, previousRun := time.Now(), time.Now()
			for {
				select {
				case <-ctx.Done():
					return nil
				default:
					trigger.Wait(previousRun)
					previousRun, lastRun = lastRun, time.Now()
					loop.RunAutoscalerOnce(ctx, autoscaler, healthCheck, lastRun, iteration)
					iteration++
				}
			}
		}

		ticker := time.NewTicker(opts.ScanInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return nil
			case runAt := <-ticker.C:
				loop.RunAutoscalerOnce(ctx, autoscaler, healthCheck, runAt, iteration)
				iteration++
			}
		}
	})); err != nil {
		klog.Fatalf("Failed to add autoscaler to manager: %v", err)
	}

	if err := mgr.Start(ctx); err != nil {
		klog.Fatalf("Manager exited with error: %v", err)
	}
}

func mustBuildAutoscaler(ctx context.Context, opts config.AutoscalingOptions, debuggingSnapshotter debuggingsnapshot.DebuggingSnapshotter, mgr manager.Manager) (core.Autoscaler, *loop.LoopTrigger) {
	kubeClient := kube_util.CreateKubeClient(opts.KubeClientOpts)
	trimManagedFields := func(obj any) (any, error) {
		if accessor, err := meta.Accessor(obj); err == nil {
			accessor.SetManagedFields(nil)
		}
		return obj, nil
	}
	informerFactory := informers.NewSharedInformerFactoryWithOptions(kubeClient, 0, informers.WithTransform(trimManagedFields))

	autoscaler, trigger, err := autoscalerbuilder.New(opts).
		WithDebuggingSnapshotter(debuggingSnapshotter).
		WithManager(mgr).
		WithKubeClient(kubeClient).
		WithInformerFactory(informerFactory).
		Build(ctx)
	if err != nil {
		klog.Fatalf("Failed to create autoscaler: %v", err)
	}
	return autoscaler, trigger
}

func main() {
	klog.InitFlags(nil)
	_ = flag.Set("legacy_stderr_threshold_behavior", "false")
	_ = flag.Set("stderrthreshold", "INFO")

	featureGate := utilfeature.DefaultMutableFeatureGate
	loggingConfig := logsapi.NewLoggingConfiguration()
	if err := logsapi.AddFeatureGates(featureGate); err != nil {
		klog.Fatalf("Failed to add logging feature flags: %v", err)
	}

	leaderElection := leaderElectionConfiguration()
	componentopts.BindLeaderElectionFlags(&leaderElection, pflag.CommandLine)
	autoscalingFlags := &flags.AutoscalingFlags{}
	autoscalingFlags.AddFlags(pflag.CommandLine)
	logsapi.AddFlags(loggingConfig, pflag.CommandLine)
	featureGate.AddFlag(pflag.CommandLine)
	kube_flag.InitFlags()

	autoscalingOpts, err := autoscalingFlags.Options()
	if err != nil {
		klog.Fatalf("Failed to parse flags: %v", err)
	}
	if autoscalingOpts.DynamicResourceAllocationEnabled != featureGate.Enabled(features.DynamicResourceAllocation) {
		if err := featureGate.SetFromMap(map[string]bool{string(features.DynamicResourceAllocation): autoscalingOpts.DynamicResourceAllocationEnabled}); err != nil {
			klog.Fatalf("Failed to set the DynamicResourceAllocation feature gate: %v", err)
		}
	}

	logs.InitLogs()
	loggingOpts, err := flags.ComputeLoggingOptions(pflag.CommandLine)
	if err != nil {
		klog.Fatalf("Failed to configure logging: %v", err)
	}
	if err := logsapi.ValidateAndApplyWithOptions(loggingConfig, loggingOpts, featureGate); err != nil {
		klog.Fatalf("Failed to apply logging configuration: %v", err)
	}
	ctrl.SetLogger(klog.NewKlogr())

	healthCheck := metrics.NewHealthCheck(autoscalingOpts.MaxInactivityTime, autoscalingOpts.MaxFailingTime, autoscalingOpts.MaxStartupTime)
	debuggingSnapshotter := debuggingsnapshot.NewDebuggingSnapshotter(autoscalingOpts.DebuggingSnapshotEnabled)
	klog.V(1).Infof("Vultr Cluster Autoscaler %s", version.ClusterAutoscalerVersion)

	go serveDiagnostics(autoscalingOpts, healthCheck, debuggingSnapshotter)

	ctx := ctrl.SetupSignalHandler()
	if !leaderElection.LeaderElect {
		run(ctx, healthCheck, debuggingSnapshotter, autoscalingOpts)
		return
	}
	runWithLeaderElection(ctx, leaderElection, healthCheck, debuggingSnapshotter, autoscalingOpts)
}

func serveDiagnostics(opts config.AutoscalingOptions, healthCheck *metrics.HealthCheck, debuggingSnapshotter debuggingsnapshot.DebuggingSnapshotter) {
	pathRecorderMux := mux.NewPathRecorderMux("cluster-autoscaler")
	pathRecorderMux.Handle("/metrics", legacyregistry.Handler())
	if opts.DebuggingSnapshotEnabled {
		pathRecorderMux.HandleFunc("/snapshotz", debuggingSnapshotter.ResponseHandler)
	}
	pathRecorderMux.HandleFunc("/health-check", healthCheck.ServeHTTP)
	if opts.EnableProfiling {
		routes.Profiling{}.Install(pathRecorderMux)
	}
	if err := http.ListenAndServe(opts.Address, pathRecorderMux); err != nil {
		klog.Fatalf("Failed to start diagnostics server: %v", err)
	}
}

func runWithLeaderElection(ctx context.Context, cfg componentbaseconfig.LeaderElectionConfiguration, healthCheck *metrics.HealthCheck, debuggingSnapshotter debuggingsnapshot.DebuggingSnapshotter, opts config.AutoscalingOptions) {
	id, err := os.Hostname()
	if err != nil {
		klog.Fatalf("Unable to get hostname: %v", err)
	}
	kubeClient := kube_util.CreateKubeClient(opts.KubeClientOpts)
	if _, err := kubeClient.CoreV1().Nodes().List(ctx, metav1.ListOptions{}); err != nil {
		klog.Fatalf("Failed to get nodes from API server: %v", err)
	}

	lock, err := resourcelock.New(
		cfg.ResourceLock,
		opts.ConfigNamespace,
		cfg.ResourceName,
		kubeClient.CoreV1(),
		kubeClient.CoordinationV1(),
		resourcelock.ResourceLockConfig{
			Identity:      id,
			EventRecorder: kube_util.CreateEventRecorder(ctx, kubeClient, opts.RecordDuplicatedEvents),
		},
	)
	if err != nil {
		klog.Fatalf("Unable to create leader election lock: %v", err)
	}

	leaderelection.RunOrDie(ctx, leaderelection.LeaderElectionConfig{
		Lock:            lock,
		LeaseDuration:   cfg.LeaseDuration.Duration,
		RenewDeadline:   cfg.RenewDeadline.Duration,
		RetryPeriod:     cfg.RetryPeriod.Duration,
		ReleaseOnCancel: true,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: func(leaderCtx context.Context) {
				run(leaderCtx, healthCheck, debuggingSnapshotter, opts)
			},
			OnStoppedLeading: func() { klog.Fatal("Lost leader election") },
		},
	})
}

func leaderElectionConfiguration() componentbaseconfig.LeaderElectionConfiguration {
	return componentbaseconfig.LeaderElectionConfiguration{
		LeaderElect:   true,
		LeaseDuration: metav1.Duration{Duration: defaultLeaseDuration},
		RenewDeadline: metav1.Duration{Duration: defaultRenewDeadline},
		RetryPeriod:   metav1.Duration{Duration: defaultRetryPeriod},
		ResourceLock:  resourcelock.LeasesResourceLock,
		ResourceName:  "cluster-autoscaler",
	}
}
