/*
Copyright 2020 MongoDB.

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

package atlasinventory

import (
	"context"

	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/mongodb/mongodb-atlas-kubernetes/pkg/api/dbaas"
	"github.com/mongodb/mongodb-atlas-kubernetes/pkg/api/v1/status"
	"github.com/mongodb/mongodb-atlas-kubernetes/pkg/controller/atlas"
	"github.com/mongodb/mongodb-atlas-kubernetes/pkg/controller/customresource"
	"github.com/mongodb/mongodb-atlas-kubernetes/pkg/controller/statushandler"
	"github.com/mongodb/mongodb-atlas-kubernetes/pkg/controller/watch"
	"github.com/mongodb/mongodb-atlas-kubernetes/pkg/controller/workflow"
)

// MongoDBAtlasInventoryReconciler reconciles a MongoDBAtlasInventory object
type MongoDBAtlasInventoryReconciler struct {
	Client client.Client
	watch.ResourceWatcher
	Log             *zap.SugaredLogger
	Scheme          *runtime.Scheme
	AtlasDomain     string
	GlobalAPISecret client.ObjectKey
	EventRecorder   record.EventRecorder
}

// Dev note: duplicate the permissions in both sections below to generate both Role and ClusterRoles

// +kubebuilder:rbac:groups=atlas.mongodb.com,resources=MongoDBAtlasInventorys,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=atlas.mongodb.com,resources=MongoDBAtlasInventorys/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

// +kubebuilder:rbac:groups=atlas.mongodb.com,namespace=default,resources=MongoDBAtlasInventorys,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=atlas.mongodb.com,namespace=default,resources=MongoDBAtlasInventorys/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",namespace=default,resources=secrets,verbs=get;list;watch

func (r *MongoDBAtlasInventoryReconciler) Reconcile(context context.Context, req ctrl.Request) (ctrl.Result, error) {
	_ = context
	log := r.Log.With("MongoDBAtlasInventory", req.NamespacedName)

	inventory := &dbaas.MongoDBAtlasInventory{}
	result := customresource.PrepareResource(r.Client, req, inventory, log)
	if !result.IsOk() {
		return result.ReconcileResult(), nil
	}
	if inventory.ConnectionSecretObjectKey() != nil {
		// Note, that we are not watching the global connection secret - seems there is no point in reconciling all
		// the services once that secret is changed
		r.EnsureResourcesAreWatched(req.NamespacedName, "Secret", log, *inventory.ConnectionSecretObjectKey())
	}
	ctx := customresource.MarkReconciliationStarted(r.Client, inventory, log)

	log.Infow("-> Starting MongoDBAtlasInventory reconciliation", "spec", inventory.Spec)

	// This update will make sure the status is always updated in case of any errors or successful result
	defer statushandler.Update(ctx, r.Client, r.EventRecorder, inventory)

	connection, err := atlas.ReadConnection(log, r.Client, r.GlobalAPISecret, inventory.ConnectionSecretObjectKey())
	if err != nil {
		result := workflow.Terminate(workflow.AtlasCredentialsNotProvided, err.Error())
		ctx.SetConditionFromResult(status.ReadyType, result)
		return result.ReconcileResult(), nil
	}
	ctx.Connection = connection

	atlasClient, err := atlas.Client(r.AtlasDomain, connection, log)
	if err != nil {
		ctx.SetConditionFromResult(status.ReadyType, workflow.Terminate(workflow.Internal, err.Error()))
		return result.ReconcileResult(), nil
	}
	ctx.Client = atlasClient

	inventoryList, result := discoverInventories(ctx)
	if !result.IsOk() {
		ctx.SetConditionFromResult(status.ReadyType, result)
		return result.ReconcileResult(), nil
	}

	ctx.
		SetConditionTrue(status.ReadyType).
		EnsureStatusOption(dbaas.MongoDBAtlasInventoryListOption(inventoryList))

	return ctrl.Result{}, nil
}

// Delete is a no-op
func (r *MongoDBAtlasInventoryReconciler) Delete(e event.DeleteEvent) error {
	return nil
}

func (r *MongoDBAtlasInventoryReconciler) SetupWithManager(mgr ctrl.Manager) error {
	c, err := controller.New("MongoDBAtlasInventory", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return err
	}

	// Watch for changes to primary resource MongoDBAtlasInventory & handle delete separately
	err = c.Watch(&source.Kind{Type: &dbaas.MongoDBAtlasInventory{}}, &watch.EventHandlerWithDelete{Controller: r}, watch.CommonPredicates())
	if err != nil {
		return err
	}

	// Watch for Connection Secrets
	err = c.Watch(&source.Kind{Type: &corev1.Secret{}}, watch.NewSecretHandler(r.WatchedResources))
	if err != nil {
		return err
	}
	return nil
}
