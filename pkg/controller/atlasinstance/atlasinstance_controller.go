/*
Copyright 2022 MongoDB.

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

package atlasinstance

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"go.uber.org/zap"

	corev1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/record"
	ptr "k8s.io/utils/pointer"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/source"

	dbaasv1alpha1 "github.com/RHEcosystemAppEng/dbaas-operator/api/v1alpha1"
	"go.mongodb.org/atlas/mongodbatlas"

	"github.com/mongodb/mongodb-atlas-kubernetes/pkg/api/dbaas"
	v1 "github.com/mongodb/mongodb-atlas-kubernetes/pkg/api/v1"
	"github.com/mongodb/mongodb-atlas-kubernetes/pkg/api/v1/common"
	"github.com/mongodb/mongodb-atlas-kubernetes/pkg/api/v1/project"
	"github.com/mongodb/mongodb-atlas-kubernetes/pkg/api/v1/provider"
	"github.com/mongodb/mongodb-atlas-kubernetes/pkg/api/v1/status"
	"github.com/mongodb/mongodb-atlas-kubernetes/pkg/controller/atlas"
	"github.com/mongodb/mongodb-atlas-kubernetes/pkg/controller/atlasinventory"
	"github.com/mongodb/mongodb-atlas-kubernetes/pkg/controller/customresource"
	"github.com/mongodb/mongodb-atlas-kubernetes/pkg/controller/watch"
	"github.com/mongodb/mongodb-atlas-kubernetes/pkg/controller/workflow"
	"github.com/mongodb/mongodb-atlas-kubernetes/pkg/util/kube"
)

const (
	instancePhaseChangedInAtlas    = "InstancePhaseChangedInAtlas"
	instancePhaseChangedInAtlasMsg = "db instance phase has changed in Atlas"
	updateAnnotationKey            = "atlas.mongodb.com/updated-at"
)

// MongoDBAtlasInstanceReconciler reconciles a MongoDBAtlasInstance object
type MongoDBAtlasInstanceReconciler struct {
	Client      client.Client
	Clientset   kubernetes.Interface
	AtlasClient *mongodbatlas.Client
	watch.ResourceWatcher
	Log             *zap.SugaredLogger
	Scheme          *runtime.Scheme
	AtlasDomain     string
	GlobalAPISecret client.ObjectKey
	EventRecorder   record.EventRecorder
}

type InstanceData struct {
	ProjectName      string
	ClusterName      string
	ProviderName     string
	RegionName       string
	InstanceSizeName string
	IPAccessList     string
}

const (
	DBaaSInstanceNameLabel         = "dbaas.redhat.com/instance-name"
	DBaaSInstanceNamespaceLabel    = "dbaas.redhat.com/instance-namespace"
	FreeClusterFailed              = "CANNOT_CREATE_FREE_CLUSTER_VIA_PUBLIC_API"
	ClusterAlreadyExistsInAtlas    = "ClusterAlreadyExistsInAtlas"
	ClusterAlreadyExistsInAtlasMsg = "Can not create the cluster as it already exists in Atlas"
)

// Dev note: duplicate the permissions in both sections below to generate both Role and ClusterRoles

// +kubebuilder:rbac:groups=dbaas.redhat.com,resources=mongodbatlasinstances,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=dbaas.redhat.com,resources=mongodbatlasinstances/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

// +kubebuilder:rbac:groups=dbaas.redhat.com,namespace=default,resources=mongodbatlasinstances,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=dbaas.redhat.com,namespace=default,resources=mongodbatlasinstances/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",namespace=default,resources=secrets,verbs=get;list;watch

func (r *MongoDBAtlasInstanceReconciler) Reconcile(cx context.Context, req ctrl.Request) (ctrl.Result, error) {
	_ = cx
	log := r.Log.With("MongoDBAtlasInstance", req.NamespacedName)
	log.Info("Reconciling MongoDBAtlasInstance")

	inst := &dbaas.MongoDBAtlasInstance{}
	if err := r.Client.Get(cx, req.NamespacedName, inst); err != nil {
		if apiErrors.IsNotFound(err) {
			// CR deleted since request queued, child objects getting GC'd, no requeue
			log.Info("MongoDBAtlasInstance resource not found, has been deleted")
			return ctrl.Result{}, nil
		}
		log.Error(err, "Error fetching MongoDBAtlasInstance for reconcile")
		return ctrl.Result{}, err
	}

	// This update will make sure the status is always updated in case of any errors or successful result
	defer func(c *dbaas.MongoDBAtlasInstance) {
		err := r.Client.Status().Update(context.Background(), c)
		if err != nil {
			log.Infof("Could not update resource status:%v", err)
		}
	}(inst)

	inventory := &dbaas.MongoDBAtlasInventory{}
	namespace := inst.Spec.InventoryRef.Namespace
	if len(namespace) == 0 {
		// Namespace is not populated in InventoryRef, default to the request's namespace
		namespace = req.Namespace
	}
	if err := r.Client.Get(cx, types.NamespacedName{Namespace: namespace, Name: inst.Spec.InventoryRef.Name}, inventory); err != nil {
		if apiErrors.IsNotFound(err) {
			// The corresponding inventory is not found, no reqeue.
			log.Info("MongoDBAtlasInventory resource not found, has been deleted")
			result := workflow.InProgress(workflow.MongoDBAtlasInstanceInventoryNotFound, "inventory not found")
			dbaas.SetInstanceCondition(inst, dbaasv1alpha1.DBaaSInstanceProviderSyncType, metav1.ConditionFalse, string(result.Reason()), result.Message())
			return ctrl.Result{}, nil
		}
		log.Error(err, "Error fetching MongoDBAtlasInventory")
		return ctrl.Result{}, err
	}
	instData, err := getInstanceData(log, inst)
	if err != nil {
		log.Error(err, "Invalid parameters")
		return ctrl.Result{}, err
	}
	result, err := r.setAtlasClient(cx, log, inst, inventory)
	if err != nil {
		return result, err
	}
	atlasProject, projectID, err := r.reconcileAtlasProject(cx, inst, instData, inventory)
	if err != nil {
		log.Error(err, "Failed to reconcile Atlas Project")
		return ctrl.Result{}, err
	}
	if !isReconcilationSkipped(atlasProject) {
		atlasProjectCond := atlasProject.CheckConditions()
		if atlasProjectCond == nil || atlasProjectCond.Type == status.IPAccessListReadyType { // AtlasProject reconciliation still on going
			log.Infof("Atlas Project for instance:%v/%v is not ready. Requeue to retry.", inst.Namespace, inst.Name)
			// Set phase to Pending
			inst.Status.Phase = dbaasv1alpha1.InstancePhasePending
			// Requeue to try again
			return ctrl.Result{Requeue: true}, nil
		}
		if atlasProjectCond.Status == corev1.ConditionFalse { // AtlasProject reconciliation failed
			dbaas.SetInstanceCondition(inst, dbaasv1alpha1.DBaaSInstanceProviderSyncType, metav1.ConditionFalse, atlasProjectCond.Reason, atlasProjectCond.Message)
			// Do not requeue
			return ctrl.Result{}, nil
		}
	} else {
		// If the atlas project reconciliation is skipped, simply update the CR status
		// with the projectID as well as a condition accordingly
		atlasProject.Status = status.AtlasProjectStatus{
			ID: projectID,
			Common: status.Common{
				Conditions: []status.Condition{
					{
						Type:               "Ready",
						Reason:             "ReconciliationSkiped",
						Status:             "True",
						LastTransitionTime: metav1.Now(),
					},
				},
			},
		}
		err = r.Client.Status().Update(cx, atlasProject)
		if err != nil {
			return ctrl.Result{}, err
		}
	}
	// Now proceed to provision the cluster
	return r.reconcileAtlasDeployment(cx, log, inst, instData, inventory, atlasProject)
}

func (r *MongoDBAtlasInstanceReconciler) setAtlasClient(cx context.Context, log *zap.SugaredLogger, inst *dbaas.MongoDBAtlasInstance, inventory *dbaas.MongoDBAtlasInventory) (ctrl.Result, error) {
	atlasConn, err := atlas.ReadConnection(log, r.Client, r.GlobalAPISecret, inventory.ConnectionSecretObjectKey())
	if err != nil {
		result := workflow.Terminate(workflow.MongoDBAtlasInventoryInputError, err.Error())
		dbaas.SetInstanceCondition(inst, dbaasv1alpha1.DBaaSInstanceProviderSyncType, metav1.ConditionFalse, string(result.Reason()), result.Message())
		return result.ReconcileResult(), nil
	}
	if r.AtlasClient == nil {
		cl, err := atlas.Client(r.AtlasDomain, atlasConn, log)
		if err != nil {
			result := workflow.Terminate(workflow.MongoDBAtlasInventoryInputError, err.Error())
			dbaas.SetInstanceCondition(inst, dbaasv1alpha1.DBaaSInstanceProviderSyncType, metav1.ConditionFalse, string(result.Reason()), result.Message())
			return result.ReconcileResult(), nil
		}
		r.AtlasClient = &cl
	}
	return ctrl.Result{}, nil
}

func (r *MongoDBAtlasInstanceReconciler) reconcileAtlasDeployment(cx context.Context, log *zap.SugaredLogger, inst *dbaas.MongoDBAtlasInstance, instData *InstanceData, inventory *dbaas.MongoDBAtlasInventory, atlasProject *v1.AtlasProject) (ctrl.Result, error) {
	if atlasProject == nil {
		return ctrl.Result{}, errors.New("there is no Atlas Project used to provision atlas cluster")
	}
	atlasDeployment := getOwnedAtlasDeployment(inst)
	if err := r.Client.Get(cx, types.NamespacedName{Namespace: atlasDeployment.Namespace, Name: atlasDeployment.Name}, atlasDeployment); err != nil {
		if apiErrors.IsNotFound(err) { // The AtlasDeployment CR does not exist
			// If the instance has been previously associated with an AtlasCluster CR, its phase is not in pending status (ie, is in creating, updating or ready)
			// This allows the operator to migrate from a previous AtlasCluster CR to an AtlasDeployment CR
			if len(inst.Status.Phase) == 0 || inst.Status.Phase == dbaasv1alpha1.InstancePhasePending || inst.Status.Phase == dbaasv1alpha1.InstancePhaseUnknown {
				_, result := atlasinventory.GetClusterInfo(r.AtlasClient, atlasProject.Spec.Name, inst.Spec.Name)
				if result.IsOk() {
					// The cluster already exists in Atlas. Mark provisioning phase as failed and return
					inst.Status.Phase = dbaasv1alpha1.InstancePhaseFailed
					dbaas.SetInstanceCondition(inst, dbaasv1alpha1.DBaaSInstanceProviderSyncType, metav1.ConditionFalse, ClusterAlreadyExistsInAtlas, ClusterAlreadyExistsInAtlasMsg)
					// No requeue
					return ctrl.Result{}, nil
				}
			} else {
				log.Info("This instance has been reconciled previously but no AtlasDeployment CR is found. Creating a new AtlasDeployment CR.")
			}
		} else {
			return ctrl.Result{}, err
		}
	}
	_, err := controllerutil.CreateOrUpdate(cx, r.Client, atlasDeployment, instanceMutateFn(atlasProject, atlasDeployment, instData))
	if err != nil {
		log.Error(err, "Failed to create or update AtlasDeployment resource")
		return ctrl.Result{}, err
	}

	// Update the status
	if err := r.Client.Get(cx, types.NamespacedName{Namespace: atlasDeployment.Namespace, Name: atlasDeployment.Name}, atlasDeployment); err != nil {
		if apiErrors.IsNotFound(err) {
			// The corresponding AtlasDeployment is not found, no reqeue.
			log.Info("AtlasDeployment resource not found, has been deleted")
			result := workflow.InProgress(workflow.MongoDBAtlasInstanceClusterNotFound, "AtlasDeployment not found")
			dbaas.SetInstanceCondition(inst, dbaasv1alpha1.DBaaSInstanceProviderSyncType, metav1.ConditionFalse, string(result.Reason()), result.Message())
			return ctrl.Result{}, nil
		}
		log.Error(err, "Error fetching AtlasDeployment")
		return ctrl.Result{}, err
	}

	stateChangedInAtlas, result := setInstanceStatusWithDeploymentInfo(r.AtlasClient, inst, atlasDeployment, instData.ProjectName)
	if !result.IsOk() {
		if stateChangedInAtlas {
			// Update an annotation in the atlas deployment resource to trigger its reconciliation
			log.Infof("Trigger AtlasDeployment reconciliation. Reason: %v", result.Message())
			_ = r.annotateAtlasDeployment(cx, atlasDeployment)
		}
		log.Infof("Error setting instance status: %v", result.Message())
		return ctrl.Result{}, errors.New(result.Message())
	}

	return ctrl.Result{}, nil
}

func (r *MongoDBAtlasInstanceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	c, err := controller.New("MongoDBAtlasInstance", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return err
	}

	// Watch for changes to primary resource MongoDBAtlasInstance & handle delete separately
	err = c.Watch(&source.Kind{Type: &dbaas.MongoDBAtlasInstance{}},
		&watch.EventHandlerWithDelete{Controller: r},
		watch.CommonPredicates())
	if err != nil {
		return err
	}

	if err != nil {
		return err
	}

	// Watch for dependent AtlasDeployment resource
	err = c.Watch(
		&source.Kind{
			Type: &v1.AtlasDeployment{},
		},
		&handler.EnqueueRequestForOwner{
			OwnerType:    &dbaas.MongoDBAtlasInstance{},
			IsController: true,
		},
	)
	if err != nil {
		return err
	}
	return nil
}

// getAtlasProject returns an AtlasProject CR
func (r *MongoDBAtlasInstanceReconciler) getAtlasProject(cx context.Context, inst *dbaas.MongoDBAtlasInstance) (atlasProject *v1.AtlasProject, err error) {
	atlasProjectList := &v1.AtlasProjectList{}
	opts := &client.ListOptions{
		LabelSelector: labels.SelectorFromSet(map[string]string{
			DBaaSInstanceNameLabel:      inst.Name,
			DBaaSInstanceNamespaceLabel: inst.Namespace,
		}),
	}
	err = r.Client.List(cx, atlasProjectList, opts)
	if err != nil {
		return
	}
	if len(atlasProjectList.Items) < 1 {
		return
	}
	atlasProject = &atlasProjectList.Items[0]
	return
}

func (r *MongoDBAtlasInstanceReconciler) reconcileAtlasProject(cx context.Context, inst *dbaas.MongoDBAtlasInstance, instData *InstanceData, inventory *dbaas.MongoDBAtlasInventory) (atlasProject *v1.AtlasProject, projectID string, err error) {
	// First check if there is already an AtlasProject resource created for this instance using labels
	atlasProject, err = r.getAtlasProject(cx, inst)
	if err != nil {
		return
	}
	if atlasProject == nil {
		// No AtlasProject resource found, create one
		project, id, err1 := r.getAtlasProjectForCreation(inst, instData, inventory)
		if err1 != nil {
			err = err1
			return
		}
		err = r.Client.Create(cx, project, &client.CreateOptions{})
		if err != nil {
			return
		}
		projectID = id
		atlasProject = project
	} else {
		projectID = atlasProject.Status.ID
	}
	return
}

// Delete implements a handler for the Delete event.
func (r *MongoDBAtlasInstanceReconciler) Delete(e event.DeleteEvent) error {
	inst, ok := e.Object.(*dbaas.MongoDBAtlasInstance)
	log := r.Log.With("MongoDBAtlasInstance", kube.ObjectKeyFromObject(inst))
	if !ok {
		log.Errorf("Ignoring malformed Delete() call (expected type %T, got %T)", &dbaas.MongoDBAtlasInstance{}, e.Object)
		return nil
	}
	// Fetch the corresponding AtlasProject resource for this instance
	cx := context.Background()
	atlasProject, err := r.getAtlasProject(cx, inst)
	if err == nil && atlasProject != nil {
		// Delete the AtlasProject resource. Note that the project will be kept in Atlas.
		err = r.Client.Delete(cx, atlasProject, &client.DeleteOptions{})
	}
	return err
}

// getAtlasProjectForCreation returns an AtlasProject object for provisioning
// No ownerref is set as the same project can be used to provision multiple clusters
// If the project already exists in Atlas, its IP access list shall remain the same
// when a new instance is provisioned. This is achieved by skipping the reconciliation
// with an annotation set in the AtlasProject CR:
// mongodb.com/atlas-reconciliation-policy: skip
func (r *MongoDBAtlasInstanceReconciler) getAtlasProjectForCreation(instance *dbaas.MongoDBAtlasInstance, data *InstanceData, inventory *dbaas.MongoDBAtlasInventory) (*v1.AtlasProject, string, error) {
	annotations := map[string]string{
		// Keep the atlas project in Atlas when local k8s AtlasProject resource is deleted
		customresource.ResourcePolicyAnnotation: customresource.ResourcePolicyKeep,
	}
	project, _, err := r.AtlasClient.Projects.GetOneProjectByName(context.Background(), data.ProjectName)
	var projectID string
	if err == nil {
		// The project exists in Atlas. We skip the Atlas Project's reconciliation so that the
		// custom resource created does not get reconciled so that it won't impact the project in Atlas.
		// This is done by setting an annotation.
		annotations[customresource.ReconciliationPolicyAnnotation] = customresource.ReconciliationPolicySkip
		projectID = project.ID
	} else if err != nil && !strings.Contains(err.Error(), "NOT_IN_GROUP") {
		return nil, "", err
	}
	secret := &corev1.Secret{}
	if err := r.Client.Get(context.Background(), *inventory.ConnectionSecretObjectKey(), secret); err != nil {
		return nil, "", err
	}

	accessList, err := parseIPAccessList(data.IPAccessList)
	if err != nil {
		return nil, "", err
	}
	return &v1.AtlasProject{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "atlas-project-",
			Namespace:    inventory.Namespace, // AtlasProject CR must be in the same namespace as the inventory
			Labels: map[string]string{
				"created-by":                "atlas-operator",
				DBaaSInstanceNameLabel:      instance.Name,
				DBaaSInstanceNamespaceLabel: instance.Namespace,
			},
			Annotations: annotations,
		},
		Spec: v1.AtlasProjectSpec{
			Name:                data.ProjectName,
			ConnectionSecret:    &common.ResourceRef{Name: inventory.Spec.CredentialsRef.Name},
			ProjectIPAccessList: accessList,
		},
	}, projectID, nil
}

func (r *MongoDBAtlasInstanceReconciler) annotateAtlasDeployment(cx context.Context, atlasDeployment *v1.AtlasDeployment) error {
	annotations := atlasDeployment.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}
	annotations[updateAnnotationKey] = time.Now().Format(time.RFC3339)
	atlasDeployment.SetAnnotations(annotations)
	return r.Client.Update(cx, atlasDeployment, &client.UpdateOptions{})
}

// getAtlasDeploymentSpec returns the spec for the desired cluster
func getAtlasDeploymentSpec(atlasProject *v1.AtlasProject, data *InstanceData) *v1.AtlasDeploymentSpec {
	var providerSettingsSpec *v1.ProviderSettingsSpec
	if data.InstanceSizeName == "M0" || data.InstanceSizeName == "M2" || data.InstanceSizeName == "M5" {
		// See Atlas documentation https://docs.atlas.mongodb.com/reference/api/clusters-create-one/
		providerSettingsSpec = &v1.ProviderSettingsSpec{
			InstanceSizeName:    data.InstanceSizeName,
			BackingProviderName: data.ProviderName,
			ProviderName:        provider.ProviderName("TENANT"),
			RegionName:          data.RegionName,
		}
	} else {
		providerSettingsSpec = &v1.ProviderSettingsSpec{
			InstanceSizeName: data.InstanceSizeName,
			ProviderName:     provider.ProviderName(data.ProviderName),
			RegionName:       data.RegionName,
		}
	}
	return &v1.AtlasDeploymentSpec{
		Project: common.ResourceRefNamespaced{Name: atlasProject.Name, Namespace: atlasProject.Namespace},
		DeploymentSpec: &v1.DeploymentSpec{
			Name:             data.ClusterName,
			ProviderSettings: providerSettingsSpec,
		},
	}
}

// getOwnedAtlasDeployment returns an AtlasDeployment object owned by the instance
func getOwnedAtlasDeployment(instance *dbaas.MongoDBAtlasInstance) *v1.AtlasDeployment {
	return &v1.AtlasDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      instance.Name,
			Namespace: instance.Namespace,
			Labels: map[string]string{
				"managed-by":      "atlas-operator",
				"owner":           instance.Name,
				"owner.kind":      instance.Kind,
				"owner.namespace": instance.Namespace,
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					UID:                instance.GetUID(),
					APIVersion:         dbaas.GroupVersion.Identifier(),
					BlockOwnerDeletion: ptr.BoolPtr(false),
					Controller:         ptr.BoolPtr(true),
					Kind:               "MongoDBAtlasInstance",
					Name:               instance.Name,
				},
			},
		},
	}
}

func getInstanceData(log *zap.SugaredLogger, inst *dbaas.MongoDBAtlasInstance) (*InstanceData, error) {
	name := strings.TrimSpace(inst.Spec.Name)
	if len(name) == 0 {
		log.Errorf("Missing %v", dbaas.ClusterNameKey)
		return nil, fmt.Errorf("missing %v", dbaas.ClusterNameKey)
	}
	projectName, ok := inst.Spec.OtherInstanceParams[dbaas.ProjectNameKey]
	if !ok || len(strings.TrimSpace(projectName)) == 0 {
		log.Errorf("Missing %v", dbaas.ProjectNameKey)
		return nil, fmt.Errorf("missing %v", dbaas.ProjectNameKey)
	}
	provider := strings.ToUpper(strings.TrimSpace(inst.Spec.CloudProvider))
	if len(provider) == 0 {
		provider = "AWS"
		log.Infof("%v is missing, default value of AWS is used", dbaas.CloudProviderKey)
	}
	region := strings.TrimSpace(inst.Spec.CloudRegion)
	if len(region) == 0 {
		switch provider {
		case "AWS":
			region = "US_EAST_1"
		case "GCE":
			region = "CENTRAL_US"
		case "AZURE":
			region = "US_WEST"
		}
		log.Infof("%v is missing, default value of %s is used", dbaas.CloudProviderKey, region)
	}
	instanceSizeName, ok := inst.Spec.OtherInstanceParams[dbaas.InstanceSizeNameKey]
	if !ok || len(strings.TrimSpace(instanceSizeName)) == 0 {
		log.Infof("%v is missing, default value of M0 is used", dbaas.InstanceSizeNameKey)
		instanceSizeName = "M0"
	}

	accessIP, ok := inst.Spec.OtherInstanceParams[dbaas.IPAccessListKey]
	if !ok || len(strings.TrimSpace(accessIP)) == 0 {
		accessIP = "0.0.0.0/0"
		log.Infof("%v is missing, default IP %s is used to allow access from everywhere.", dbaas.IPAccessListKey, accessIP)
	}

	return &InstanceData{
		ProjectName:      strings.TrimSpace(projectName),
		ClusterName:      name,
		ProviderName:     provider,
		RegionName:       region,
		InstanceSizeName: strings.TrimSpace(instanceSizeName),
		IPAccessList:     strings.TrimSpace(accessIP),
	}, nil
}

func instanceMutateFn(atlasProject *v1.AtlasProject, atlasDeployment *v1.AtlasDeployment, data *InstanceData) controllerutil.MutateFn {
	return func() error {
		atlasDeployment.Spec = *getAtlasDeploymentSpec(atlasProject, data)
		return nil
	}
}

func setInstanceStatusWithDeploymentInfo(atlasClient *mongodbatlas.Client, inst *dbaas.MongoDBAtlasInstance, atlasDeployment *v1.AtlasDeployment, project string) (bool, workflow.Result) {
	instInfo, result := atlasinventory.GetClusterInfo(atlasClient, project, inst.Spec.Name)
	if result.IsOk() {
		// Stores the phase info in inst.Status.Phase and remove from instInfo.InstanceInf map
		inst.Status.Phase = dbaasv1alpha1.DBaasInstancePhase(instInfo.InstanceInfo[dbaas.ProvisionPhaseKey])
		delete(instInfo.InstanceInfo, dbaas.ProvisionPhaseKey)
		inst.Status.InstanceID = instInfo.InstanceID
		inst.Status.InstanceInfo = instInfo.InstanceInfo
	} else {
		inst.Status.Phase = dbaasv1alpha1.InstancePhasePending
		inst.Status.InstanceID = ""
		inst.Status.InstanceInfo = nil
	}
	statusFound := false
	for _, cond := range atlasDeployment.Status.Conditions {
		if cond.Type == status.DeploymentReadyType {
			statusFound = true
			if cond.Status == corev1.ConditionTrue {
				if inst.Status.Phase == dbaasv1alpha1.InstancePhaseReady {
					dbaas.SetInstanceCondition(inst, dbaasv1alpha1.DBaaSInstanceProviderSyncType, metav1.ConditionStatus(cond.Status), "Ready", cond.Message)
					return false, result
				}
				dbaas.SetInstanceCondition(inst, dbaasv1alpha1.DBaaSInstanceProviderSyncType, metav1.ConditionFalse, instancePhaseChangedInAtlas, instancePhaseChangedInAtlasMsg)
				return true, result
			} else {
				if strings.Contains(cond.Message, FreeClusterFailed) {
					inst.Status.Phase = dbaasv1alpha1.InstancePhaseFailed
				}
				dbaas.SetInstanceCondition(inst, dbaasv1alpha1.DBaaSInstanceProviderSyncType, metav1.ConditionStatus(cond.Status), cond.Reason, cond.Message)
			}
		}
	}
	if !statusFound {
		dbaas.SetInstanceCondition(inst, dbaasv1alpha1.DBaaSInstanceProviderSyncType, metav1.ConditionFalse, string(dbaasv1alpha1.InstancePhasePending), "Waiting for cluster creation to start")
	}
	return false, result
}

func parseIPAccessList(accessListStr string) ([]project.IPAccessList, error) {
	accessList := []project.IPAccessList{}
	ranges := strings.Split(accessListStr, " ")
	for _, r := range ranges {
		ip := net.ParseIP(r)
		if ip != nil {
			accessList = append(accessList, project.IPAccessList{IPAddress: r})
		} else {
			_, _, err := net.ParseCIDR(r)
			if err == nil {
				accessList = append(accessList, project.IPAccessList{CIDRBlock: r})
			} else {
				return nil, fmt.Errorf("parsing error for ip or ip range: %s", r)
			}
		}
	}
	return accessList, nil
}

func isReconcilationSkipped(atlasProject *v1.AtlasProject) bool {
	return atlasProject.Annotations[customresource.ReconciliationPolicyAnnotation] == customresource.ReconciliationPolicySkip
}
