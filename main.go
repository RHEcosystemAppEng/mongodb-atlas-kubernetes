/*
Copyright 2020 The Kubernetes authors.

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
	"flag"
	"log"
	"os"
	"time"

	"github.com/go-logr/zapr"
	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	ctrzap "sigs.k8s.io/controller-runtime/pkg/log/zap"

	dbaasv1alpha1 "github.com/RHEcosystemAppEng/dbaas-operator/api/v1alpha1"

	dbaas "github.com/mongodb/mongodb-atlas-kubernetes/pkg/api/dbaas"
	mdbv1 "github.com/mongodb/mongodb-atlas-kubernetes/pkg/api/v1"
	"github.com/mongodb/mongodb-atlas-kubernetes/pkg/controller/atlas"
	"github.com/mongodb/mongodb-atlas-kubernetes/pkg/controller/atlascluster"
	"github.com/mongodb/mongodb-atlas-kubernetes/pkg/controller/atlasconnection"
	"github.com/mongodb/mongodb-atlas-kubernetes/pkg/controller/atlasdatabaseuser"
	"github.com/mongodb/mongodb-atlas-kubernetes/pkg/controller/atlasinventory"
	"github.com/mongodb/mongodb-atlas-kubernetes/pkg/controller/atlasproject"
	"github.com/mongodb/mongodb-atlas-kubernetes/pkg/controller/connectionsecret"
	"github.com/mongodb/mongodb-atlas-kubernetes/pkg/controller/dbaasprovider"
	"github.com/mongodb/mongodb-atlas-kubernetes/pkg/controller/watch"
	"github.com/mongodb/mongodb-atlas-kubernetes/pkg/util/kube"
	// +kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")

	// Set by the linker during link time.
	version = "unknown"
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(mdbv1.AddToScheme(scheme))

	utilruntime.Must(dbaas.AddToScheme(scheme))

	utilruntime.Must(dbaasv1alpha1.AddToScheme(scheme))

	// +kubebuilder:scaffold:scheme

	atlas.ProductVersion = version
}

func main() {
	// controller-runtime/pkg/log/zap is a wrapper over zap that implements logr
	// logr looks quite limited in functionality so we better use Zap directly.
	// Though we still need the controller-runtime library and go-logr/zapr as they are used in controller-runtime
	// logging
	logger := ctrzap.NewRaw(ctrzap.UseDevMode(true), ctrzap.StacktraceLevel(zap.ErrorLevel))

	config := parseConfiguration(logger.Sugar())

	ctrl.SetLogger(zapr.NewLogger(logger))

	logger.Sugar().Infof("MongoDB Atlas Operator version %s", version)

	syncPeriod := time.Hour * 3
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		MetricsBindAddress:     config.MetricsAddr,
		Port:                   9443,
		HealthProbeBindAddress: config.ProbeAddr,
		LeaderElection:         config.EnableLeaderElection,
		LeaderElectionID:       "06d035fb.mongodb.com",
		Namespace:              config.WatchedNamespaces,
		SyncPeriod:             &syncPeriod,
		NewCache: cache.BuilderWithOptions(cache.Options{
			SelectorsByObject: cache.SelectorsByObject{
				&corev1.Secret{}: {
					Label: labels.SelectorFromSet(labels.Set{
						connectionsecret.VendorKey: connectionsecret.VendorVal,
					}),
				},
			},
		}),
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	cfg := mgr.GetConfig()
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		setupLog.Error(err, "unable to create clientset")
		os.Exit(1)
	}

	if err = (&dbaasprovider.DBaaSProviderReconciler{
		Client:    mgr.GetClient(),
		Scheme:    mgr.GetScheme(),
		Log:       logger.Named("controllers").Named("DBaaSProvider").Sugar(),
		Clientset: clientset,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "DBaaSProvider")
		os.Exit(1)
	}

	if err = (&atlasinventory.MongoDBAtlasInventoryReconciler{
		Client:          mgr.GetClient(),
		Log:             logger.Named("controllers").Named("MongoDBAtlasInventory").Sugar(),
		Scheme:          mgr.GetScheme(),
		AtlasDomain:     config.AtlasDomain,
		ResourceWatcher: watch.NewResourceWatcher(),
		GlobalAPISecret: config.GlobalAPISecret,
		EventRecorder:   mgr.GetEventRecorderFor("MongoDBAtlasInventory"),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "MongoDBAtlasInventory")
		os.Exit(1)
	}

	if err = (&atlasconnection.MongoDBAtlasConnectionReconciler{
		Client:          mgr.GetClient(),
		Clientset:       clientset,
		Log:             logger.Named("controllers").Named("MongoDBAtlasConnection").Sugar(),
		Scheme:          mgr.GetScheme(),
		AtlasDomain:     config.AtlasDomain,
		ResourceWatcher: watch.NewResourceWatcher(),
		GlobalAPISecret: config.GlobalAPISecret,
		EventRecorder:   mgr.GetEventRecorderFor("MongoDBAtlasConnection"),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "MongoDBAtlasConnection")
		os.Exit(1)
	}

	if err = (&atlascluster.AtlasClusterReconciler{
		Client:          mgr.GetClient(),
		Log:             logger.Named("controllers").Named("AtlasCluster").Sugar(),
		Scheme:          mgr.GetScheme(),
		AtlasDomain:     config.AtlasDomain,
		GlobalAPISecret: config.GlobalAPISecret,
		EventRecorder:   mgr.GetEventRecorderFor("AtlasCluster"),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "AtlasCluster")
		os.Exit(1)
	}

	if err = (&atlasproject.AtlasProjectReconciler{
		Client:          mgr.GetClient(),
		Log:             logger.Named("controllers").Named("AtlasProject").Sugar(),
		Scheme:          mgr.GetScheme(),
		AtlasDomain:     config.AtlasDomain,
		ResourceWatcher: watch.NewResourceWatcher(),
		GlobalAPISecret: config.GlobalAPISecret,
		EventRecorder:   mgr.GetEventRecorderFor("AtlasProject"),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "AtlasProject")
		os.Exit(1)
	}

	if err = (&atlasdatabaseuser.AtlasDatabaseUserReconciler{
		Client:          mgr.GetClient(),
		Log:             logger.Named("controllers").Named("AtlasDatabaseUser").Sugar(),
		Scheme:          mgr.GetScheme(),
		AtlasDomain:     config.AtlasDomain,
		ResourceWatcher: watch.NewResourceWatcher(),
		GlobalAPISecret: config.GlobalAPISecret,
		EventRecorder:   mgr.GetEventRecorderFor("AtlasDatabaseUser"),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "AtlasDatabaseUser")
		os.Exit(1)
	}
	// +kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("health", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("check", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

type Config struct {
	AtlasDomain          string
	EnableLeaderElection bool
	MetricsAddr          string
	WatchedNamespaces    string
	ProbeAddr            string
	GlobalAPISecret      client.ObjectKey
}

// ParseConfiguration fills the 'OperatorConfig' from the flags passed to the program
func parseConfiguration(log *zap.SugaredLogger) Config {
	var globalAPISecretName string
	config := Config{}
	flag.StringVar(&config.AtlasDomain, "atlas-domain", "https://cloud.mongodb.com", "the Atlas URL domain name (no slash in the end).")
	flag.StringVar(&config.MetricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&config.ProbeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.StringVar(&globalAPISecretName, "global-api-secret-name", "", "The name of the Secret that contains Atlas API keys. "+
		"It is used by the Operator if AtlasProject configuration doesn't contain API key reference. Defaults to <deployment_name>-api-key.")
	flag.BoolVar(&config.EnableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")

	flag.Parse()

	config.GlobalAPISecret = operatorGlobalKeySecretOrDefault(globalAPISecretName)

	// dev note: we pass the watched namespace as the env variable to use the Kubernetes Downward API. Unfortunately
	// there is no way to use it for container arguments
	watchedNamespace := os.Getenv("WATCH_NAMESPACE")
	if watchedNamespace != "" {
		log.Infof("The Operator is watching the namespace %s", watchedNamespace)
	}
	config.WatchedNamespaces = watchedNamespace
	return config
}

func operatorGlobalKeySecretOrDefault(secretNameOverride string) client.ObjectKey {
	secretName := secretNameOverride
	if secretName == "" {
		operatorPodName := os.Getenv("OPERATOR_POD_NAME")
		if operatorPodName == "" {
			log.Fatal(`"OPERATOR_POD_NAME" environment variable must be set!`)
		}
		deploymentName, err := kube.ParseDeploymentNameFromPodName(operatorPodName)
		if err != nil {
			log.Fatalf(`Failed to get Operator Deployment name from "OPERATOR_POD_NAME" environment variable: %s`, err.Error())
		}
		secretName = deploymentName + "-api-key"
	}
	operatorNamespace := os.Getenv("OPERATOR_NAMESPACE")
	if operatorNamespace == "" {
		log.Fatal(`"OPERATOR_NAMESPACE" environment variable must be set!`)
	}

	return client.ObjectKey{Namespace: operatorNamespace, Name: secretName}
}
