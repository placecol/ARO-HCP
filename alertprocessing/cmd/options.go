// Copyright 2026 Microsoft Corporation
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cmd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	_ "k8s.io/component-base/metrics/prometheus/clientgo"

	"github.com/spf13/cobra"

	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	"k8s.io/component-base/metrics/legacyregistry"
	"k8s.io/klog/v2"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/alertprocessingrules/armalertprocessingrules"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/prometheusrulegroups/armprometheusrulegroups"

	sharedleaderelection "github.com/Azure/ARO-HCP/internal/leaderelection"

	alertprocessingv1alpha1 "github.com/Azure/ARO-HCP/alertprocessing/pkg/apis/alertprocessing/v1alpha1"
	"github.com/Azure/ARO-HCP/alertprocessing/pkg/controllers/aprreconcile"
	"github.com/Azure/ARO-HCP/alertprocessing/pkg/controllers/catalogsync"
	alertprocessingclient "github.com/Azure/ARO-HCP/alertprocessing/pkg/generated/clientset/versioned"
	alertprocessinginformers "github.com/Azure/ARO-HCP/alertprocessing/pkg/generated/informers/externalversions"
)

const (
	// LeaderElectionLockName is the lease name used for leader election.
	LeaderElectionLockName = "alertprocessing-controller-leader"
)

// RawControllerOptions holds the raw, unvalidated controller configuration.
type RawControllerOptions struct {
	HealthAddress string
	Kubeconfig    string
	Namespace     string
	Workers       int
	LogVerbosity  int

	SubscriptionID        string
	RegionalResourceGroup string
	HCPsAMWResourceID     string
	ServicesAMWResourceID string
	DefaultActionGroupID  string
	CatalogPollInterval   time.Duration
}

// DefaultControllerOptions returns the default controller options.
func DefaultControllerOptions() *RawControllerOptions {
	return &RawControllerOptions{
		HealthAddress:       ":8080",
		Workers:             2,
		CatalogPollInterval: 5 * time.Minute,
	}
}

// BindFlags binds the command-line flags.
func (o *RawControllerOptions) BindFlags(cmd *cobra.Command) error {
	cmd.Flags().StringVar(&o.HealthAddress, "health-address", o.HealthAddress, "The bind address for the health/metrics server (e.g., ':8080')")
	cmd.Flags().StringVar(&o.Kubeconfig, "kubeconfig", "", "Path to a kubeconfig. Optional; in-cluster config is used when empty.")
	cmd.Flags().StringVar(&o.Namespace, "namespace", os.Getenv("POD_NAMESPACE"), "The namespace where the controller runs (used for leader election).")
	cmd.Flags().IntVar(&o.Workers, "workers", o.Workers, "Number of reconcile workers for the APR reconcile loop.")
	cmd.Flags().IntVar(&o.LogVerbosity, "log-verbosity", o.LogVerbosity, "Log verbosity; 0 is INFO, higher is more verbose.")
	cmd.Flags().StringVar(&o.SubscriptionID, "subscription-id", os.Getenv("AZURE_SUBSCRIPTION_ID"), "Azure subscription ID of the regional resources.")
	cmd.Flags().StringVar(&o.RegionalResourceGroup, "regional-rg", "", "Resource group where the managed Azure Alert Processing Rule resources live.")
	cmd.Flags().StringVar(&o.HCPsAMWResourceID, "hcps-amw-id", "", "ARM resource ID of the hcps Azure Monitor Workspace.")
	cmd.Flags().StringVar(&o.ServicesAMWResourceID, "services-amw-id", "", "ARM resource ID of the services Azure Monitor Workspace.")
	cmd.Flags().StringVar(&o.DefaultActionGroupID, "default-action-group-id", "", "ARM resource ID of the default (ICM) action group applied to every rule.")
	cmd.Flags().DurationVar(&o.CatalogPollInterval, "catalog-poll-interval", o.CatalogPollInterval, "How often loop 1 polls Azure for alert rules.")
	return nil
}

type validatedControllerOptions struct {
	*RawControllerOptions
}

// ValidatedControllerOptions is a RawControllerOptions that passed validation.
type ValidatedControllerOptions struct {
	*validatedControllerOptions
}

type completedControllerOptions struct {
	catalogController *catalogsync.Controller
	aprController     *aprreconcile.Controller
	informers         alertprocessinginformers.SharedInformerFactory

	workers            int
	healthAddress      string
	leaderElectionLock resourcelock.Interface
}

// ControllerOptions is a fully-completed, runnable configuration.
type ControllerOptions struct {
	*completedControllerOptions
}

// Validate checks the raw options.
func (o *RawControllerOptions) Validate(ctx context.Context) (*ValidatedControllerOptions, error) {
	if o.Namespace == "" {
		return nil, fmt.Errorf("--namespace is required")
	}
	if o.HealthAddress == "" {
		return nil, fmt.Errorf("--health-address is required")
	}
	if o.LogVerbosity < 0 {
		return nil, fmt.Errorf("--log-verbosity must be >= 0")
	}
	if o.SubscriptionID == "" {
		return nil, fmt.Errorf("--subscription-id is required")
	}
	if o.RegionalResourceGroup == "" {
		return nil, fmt.Errorf("--regional-rg is required")
	}
	if o.HCPsAMWResourceID == "" || o.ServicesAMWResourceID == "" {
		return nil, fmt.Errorf("--hcps-amw-id and --services-amw-id are required")
	}
	if o.DefaultActionGroupID == "" {
		return nil, fmt.Errorf("--default-action-group-id is required")
	}
	if o.CatalogPollInterval <= 0 {
		return nil, fmt.Errorf("--catalog-poll-interval must be positive")
	}
	return &ValidatedControllerOptions{validatedControllerOptions: &validatedControllerOptions{RawControllerOptions: o}}, nil
}

// Complete builds the clients and controllers.
func (o *ValidatedControllerOptions) Complete(ctx context.Context) (*ControllerOptions, error) {
	azureCredential, err := azidentity.NewDefaultAzureCredential(&azidentity.DefaultAzureCredentialOptions{
		RequireAzureTokenCredentials: true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create Azure credential: %w", err)
	}

	kubeConfig, err := o.buildKubeConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to build kubeconfig: %w", err)
	}

	apClient, err := alertprocessingclient.NewForConfig(kubeConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create alertprocessing clientset: %w", err)
	}
	informers := alertprocessinginformers.NewSharedInformerFactory(apClient, 10*time.Minute)

	aprAzureClient, err := armalertprocessingrules.NewClient(o.SubscriptionID, azureCredential, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create alert processing rules client: %w", err)
	}
	ruleGroupsClient, err := armprometheusrulegroups.NewClient(o.SubscriptionID, azureCredential, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create prometheus rule groups client: %w", err)
	}

	scopes := []catalogsync.ScopeAMW{
		{Scope: alertprocessingv1alpha1.ScopeHCPs, AMWResourceID: o.HCPsAMWResourceID},
		{Scope: alertprocessingv1alpha1.ScopeServices, AMWResourceID: o.ServicesAMWResourceID},
	}
	scopeToAMW := map[alertprocessingv1alpha1.Scope]string{
		alertprocessingv1alpha1.ScopeHCPs:     o.HCPsAMWResourceID,
		alertprocessingv1alpha1.ScopeServices: o.ServicesAMWResourceID,
	}

	defaultActionGroup := o.DefaultActionGroupID
	actionGroupResolver := func(_ alertprocessingv1alpha1.Scope, _ string) string {
		// Single region default for the spike. A per-rule override map can be layered here.
		return defaultActionGroup
	}
	scopeResolver := func(scope alertprocessingv1alpha1.Scope) (string, error) {
		id, ok := scopeToAMW[scope]
		if !ok {
			return "", fmt.Errorf("no AMW configured for scope %q", scope)
		}
		return id, nil
	}

	catalogController := catalogsync.NewController(
		apClient,
		informers,
		catalogsync.NewAzureRuleLister(ruleGroupsClient),
		scopes,
		actionGroupResolver,
		o.CatalogPollInterval,
	)
	aprController := aprreconcile.NewController(
		apClient,
		informers,
		aprreconcile.NewAzureAPRClient(aprAzureClient, o.RegionalResourceGroup),
		scopeResolver,
	)

	hostname, err := os.Hostname()
	if err != nil {
		return nil, fmt.Errorf("failed to get hostname for leader election: %w", err)
	}
	leaderElectionLock, err := sharedleaderelection.NewLeaderElectionLock(hostname, kubeConfig, o.Namespace, LeaderElectionLockName, sharedleaderelection.RecommendedRenewDeadline)
	if err != nil {
		return nil, fmt.Errorf("failed to create leader election lock: %w", err)
	}

	return &ControllerOptions{
		completedControllerOptions: &completedControllerOptions{
			catalogController:  catalogController,
			aprController:      aprController,
			informers:          informers,
			workers:            o.Workers,
			healthAddress:      o.HealthAddress,
			leaderElectionLock: leaderElectionLock,
		},
	}, nil
}

func (o *ValidatedControllerOptions) buildKubeConfig() (*rest.Config, error) {
	config, err := rest.InClusterConfig()
	if err == nil {
		klog.V(6).Info("Using in-cluster kubeconfig")
		return config, nil
	}

	klog.V(6).Info("Not running in-cluster, using out-of-cluster kubeconfig")
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if o.Kubeconfig != "" {
		loadingRules.ExplicitPath = o.Kubeconfig
	}
	kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, &clientcmd.ConfigOverrides{})
	config, err = kubeConfig.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to build kubeconfig from loading rules: %w", err)
	}
	return config, nil
}

// Run starts the health server and the controllers under leader election.
func (o *ControllerOptions) Run(ctx context.Context) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(fmt.Errorf("Run returned"))

	logger := klog.FromContext(ctx)

	var (
		mu   sync.Mutex
		errs []error
		wg   sync.WaitGroup
	)

	wg.Add(1)
	go func() {
		defer cancel(fmt.Errorf("health server exited"))
		defer wg.Done()
		defer utilruntime.HandleCrash()
		mux := http.NewServeMux()
		mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
		mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
		mux.Handle("/metrics", legacyregistry.Handler())
		server := &http.Server{Addr: o.healthAddress, Handler: mux}
		if err := runHTTPServer(ctx, server, "health server"); err != nil {
			mu.Lock()
			errs = append(errs, err)
			mu.Unlock()
		}
	}()

	wg.Add(1)
	go func() {
		defer cancel(fmt.Errorf("leader election exited"))
		defer wg.Done()
		defer utilruntime.HandleCrash()
		if err := o.runControllersUnderLeaderElection(ctx); err != nil {
			mu.Lock()
			errs = append(errs, err)
			mu.Unlock()
		}
	}()

	wg.Wait()
	logger.Info("alertprocessing stopped")
	return errors.Join(errs...)
}

func (o *ControllerOptions) runControllersUnderLeaderElection(ctx context.Context) error {
	logger := klog.FromContext(ctx)

	leaderElectionConfig := leaderelection.LeaderElectionConfig{
		Lock:          o.leaderElectionLock,
		LeaseDuration: sharedleaderelection.RecommendedLeaseDuration,
		RenewDeadline: sharedleaderelection.RecommendedRenewDeadline,
		RetryPeriod:   sharedleaderelection.RecommendedRetryPeriod,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: func(ctx context.Context) {
				logger.Info("acquired leader election lease; starting informers and controllers")
				o.informers.Start(ctx.Done())
				if ok := cache.WaitForCacheSync(ctx.Done(), o.informers.Alertprocessing().V1alpha1().AlertProcessingRules().Informer().HasSynced); !ok {
					logger.Info("informer caches did not sync; aborting controller startup")
					return
				}

				go func() {
					defer utilruntime.HandleCrash()
					if err := o.aprController.Run(ctx, o.workers); err != nil {
						logger.Error(err, "APR reconcile controller failed")
					}
				}()
				go func() {
					defer utilruntime.HandleCrash()
					o.catalogController.Run(ctx)
				}()

				<-ctx.Done()
			},
			OnStoppedLeading: func() {
				logger.Info("lost leader election lease")
			},
		},
		ReleaseOnCancel: true,
		Name:            LeaderElectionLockName,
	}

	sharedleaderelection.LogLeaseProperties(logger, leaderElectionConfig)

	le, err := leaderelection.NewLeaderElector(leaderElectionConfig)
	if err != nil {
		return err
	}
	le.Run(ctx)
	return nil
}

func runHTTPServer(ctx context.Context, server *http.Server, name string) error {
	logger := klog.FromContext(ctx)

	done := make(chan struct{})
	defer close(done)
	go func() {
		defer utilruntime.HandleCrash()
		select {
		case <-ctx.Done():
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 31*time.Second)
			defer shutdownCancel()
			logger.Info(fmt.Sprintf("shutting down %s", name))
			if err := server.Shutdown(shutdownCtx); err != nil {
				logger.Error(err, fmt.Sprintf("failed to shut down %s", name))
			}
		case <-done:
		}
	}()

	logger.Info(fmt.Sprintf("%s listening on %s", name, server.Addr))
	err := server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
