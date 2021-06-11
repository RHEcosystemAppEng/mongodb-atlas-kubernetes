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

package atlasconnection

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/record"
	ptr "k8s.io/utils/pointer"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/mongodb/mongodb-atlas-kubernetes/pkg/api/dbaas"
	mdbv1 "github.com/mongodb/mongodb-atlas-kubernetes/pkg/api/v1"
	"github.com/mongodb/mongodb-atlas-kubernetes/pkg/api/v1/status"
	"github.com/mongodb/mongodb-atlas-kubernetes/pkg/controller/atlas"
	"github.com/mongodb/mongodb-atlas-kubernetes/pkg/controller/customresource"
	"github.com/mongodb/mongodb-atlas-kubernetes/pkg/controller/statushandler"
	"github.com/mongodb/mongodb-atlas-kubernetes/pkg/controller/watch"
	"github.com/mongodb/mongodb-atlas-kubernetes/pkg/controller/workflow"
	"github.com/mongodb/mongodb-atlas-kubernetes/pkg/util/kube"
	"go.mongodb.org/atlas/mongodbatlas"
)

const (
	DBUserNameKey = "username"
	DBPasswordKey = "password"
)

// MongoDBAtlasConnectionReconciler reconciles a MongoDBAtlasConnection object
type MongoDBAtlasConnectionReconciler struct {
	Client    client.Client
	Clientset *kubernetes.Clientset
	watch.ResourceWatcher
	Log             *zap.SugaredLogger
	Scheme          *runtime.Scheme
	AtlasDomain     string
	GlobalAPISecret client.ObjectKey
	EventRecorder   record.EventRecorder
}

// Dev note: duplicate the permissions in both sections below to generate both Role and ClusterRoles

// +kubebuilder:rbac:groups=atlas.mongodb.com,resources=MongoDBAtlasConnections,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=atlas.mongodb.com,resources=MongoDBAtlasConnections/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

// +kubebuilder:rbac:groups=atlas.mongodb.com,namespace=default,resources=MongoDBAtlasConnections,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=atlas.mongodb.com,namespace=default,resources=MongoDBAtlasConnections/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",namespace=default,resources=secrets,verbs=get;list;watch

func (r *MongoDBAtlasConnectionReconciler) Reconcile(cx context.Context, req ctrl.Request) (ctrl.Result, error) {
	_ = cx
	log := r.Log.With("MongoDBAtlasConnection", req.NamespacedName)

	conn := &dbaas.MongoDBAtlasConnection{}
	result := customresource.PrepareResource(r.Client, req, conn, log)
	if !result.IsOk() {
		return result.ReconcileResult(), nil
	}

	if isReadyForBinding(conn) {
		// Already ready for binding, no further reconciliation is required
		return ctrl.Result{}, nil
	}

	ctx := customresource.MarkReconciliationStarted(r.Client, conn, log)

	log.Infow("-> Starting MongoDBAtlasInventory reconciliation", "spec", conn.Spec)

	// This update will make sure the status is always updated in case of any errors or successful result
	defer statushandler.Update(ctx, r.Client, r.EventRecorder, conn)

	inventory := &dbaas.MongoDBAtlasInventory{}

	result = getResource(r.Client, req.Namespace, conn.Spec.ConnectionRef.Name, inventory, log)
	if !result.IsOk() {
		result := workflow.Terminate(workflow.MongoDBAtlasConnectionNotFound, "inventory not found")
		ctx.SetConditionFromResult(status.MongoDBAtlasConnectionReadyType, result)
		return result.ReconcileResult(), nil
	}

	if !isInventoryReady(inventory) {
		//The corresponding inventory is not ready yet, requeue
		result := workflow.InProgress(workflow.MongoDBAtlasConnectionInventoryNotReady, "inventory not ready")
		ctx.SetConditionFromResult(status.MongoDBAtlasConnectionReadyType, result)
		return result.ReconcileResult(), nil
	}

	// Retrieve the instance from inventory based on instanceID
	instance := getInstance(inventory, conn.Spec.InstanceID)
	if instance == nil {
		result := workflow.Terminate(workflow.MongoDBAtlasConnectionNotFound, "Atlas project not found")
		ctx.SetConditionFromResult(status.MongoDBAtlasConnectionReadyType, result)
		return result.ReconcileResult(), nil
	}

	projectID := instance.InstanceInfo.ProjectID
	connectionHost := getHost(instance)

	// Generate a db username and password
	dbUserName := fmt.Sprintf("atlas-db-user-%v", time.Now().UnixNano())
	dbPassword := "testpassword"

	//Create the db user in Atlas
	res, isCreated := r.createDBUserInAtlas(ctx, projectID, dbUserName, dbPassword, inventory, log)
	if !isCreated {
		return res, nil
	}

	//Now create a secret to store the password locally
	secret := getOwnedSecret(conn, dbUserName, dbPassword)
	secretCreated, err := r.Clientset.CoreV1().Secrets(req.Namespace).Create(context.Background(), secret, metav1.CreateOptions{})
	if err != nil {
		result := workflow.Terminate(workflow.MongoDBAtlasConnectionBackendError, err.Error())
		ctx.SetConditionFromResult(status.MongoDBAtlasConnectionReadyType, result)
		return ctrl.Result{}, nil
	}

	// Update the status
	ctx.
		SetConditionTrue(status.ReadyType).
		SetConditionTrue(status.MongoDBAtlasConnectionReadyType).
		EnsureStatusOption(dbaas.AtlasConnectionHostOption(connectionHost)).
		EnsureStatusOption(dbaas.AtlasConnectionCredentialsRefOption(secretCreated.Name)).
		EnsureStatusOption(dbaas.AtlasConnectionStringOption(instance.InstanceInfo.ConnectionString))
	return ctrl.Result{}, nil
}

func (r *MongoDBAtlasConnectionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	c, err := controller.New("MongoDBAtlasConnection", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return err
	}

	// Watch for changes to primary resource MongoDBAtlasConnection & handle delete separately
	err = c.Watch(&source.Kind{Type: &dbaas.MongoDBAtlasConnection{}}, &watch.EventHandlerWithDelete{Controller: r}, watch.CommonPredicates())
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

// Delete implements a handler for the Delete event.
func (r *MongoDBAtlasConnectionReconciler) Delete(e event.DeleteEvent) error {
	conn, ok := e.Object.(*dbaas.MongoDBAtlasConnection)
	if !ok {
		r.Log.Errorf("Ignoring malformed Delete() call (expected type %T, got %T)", &dbaas.MongoDBAtlasConnection{}, e.Object)
		return nil
	}
	log := r.Log.With("MongoDBAtlasConnection", kube.ObjectKeyFromObject(conn))
	log.Infow("-> Starting MongoDBAtlasConnection deletion", "spec", conn.Spec)
	secret, err := r.getSecret(conn.Namespace, conn.Status.CredentialsRef.Name)
	if err != nil {
		r.Log.Errorf("No db usernmae found for deletion. Deletion done.")
		return nil
	}
	dbUserName := string(secret.Data[DBUserNameKey])
	inventory := &dbaas.MongoDBAtlasInventory{}
	result := getResource(r.Client, conn.Namespace, conn.Spec.ConnectionRef.Name, inventory, log)
	if !result.IsOk() {
		log.Infow("No inventory reference object found. Deletion done.", "spec", conn.Spec)
		return nil
	}

	instance := getInstance(inventory, conn.Spec.InstanceID)
	if instance == nil {
		log.Infow("No instance object found. Deletion done.", "spec", conn.Spec)
		return nil
	}

	if err := r.deleteDBUserFromAtlas(conn, instance.InstanceInfo.ProjectID, dbUserName, inventory, log); err != nil {
		log.Errorf("Failed to remove cluster from Atlas: %s", err)
	}

	//Delete the secret
	if err := r.Client.Delete(context.Background(), secret); err != nil {
		log.Errorw("Failed to delete secret", "secretName", secret.Name, "error", err)
	}
	log.Infow("Deletion done.", "spec", conn.Spec)
	return nil
}

//getSecret gets a secret object
func (r *MongoDBAtlasConnectionReconciler) getSecret(namespace, name string) (*corev1.Secret, error) {
	return r.Clientset.CoreV1().Secrets(namespace).Get(context.Background(), name, metav1.GetOptions{})
}

//createDBUserInAtlas create the database user in Atlas
func (r *MongoDBAtlasConnectionReconciler) createDBUserInAtlas(ctx *workflow.Context, projectID, dbUserName, dbPassword string, inventory *dbaas.MongoDBAtlasInventory, log *zap.SugaredLogger) (ctrl.Result, bool) {
	dbUser := &mongodbatlas.DatabaseUser{
		DatabaseName: "admin",
		GroupID:      projectID,
		Roles: []mongodbatlas.Role{
			{
				DatabaseName: "admin",
				RoleName:     "readWriteAnyDatabase",
			},
		},
		Username: dbUserName,
		Password: dbPassword,
	}

	atlasConnection, err := atlas.ReadConnection(log, r.Client, r.GlobalAPISecret, inventory.ConnectionSecretObjectKey())
	if err != nil {
		result := workflow.Terminate(workflow.MongoDBAtlasConnectionAuthenticationError, err.Error())
		ctx.SetConditionFromResult(status.MongoDBAtlasConnectionReadyType, result)
		return result.ReconcileResult(), false
	}
	atlasClient, err := atlas.Client(r.AtlasDomain, atlasConnection, log)
	if err != nil {
		result := workflow.Terminate(workflow.MongoDBAtlasConnectionBackendError, err.Error())
		ctx.SetConditionFromResult(status.MongoDBAtlasConnectionReadyType, result)
		return result.ReconcileResult(), false
	}

	// Try to create the db user
	if _, _, err := atlasClient.DatabaseUsers.Create(context.Background(), projectID, dbUser); err != nil {
		result := workflow.Terminate(workflow.DatabaseUserNotCreatedInAtlas, err.Error())
		ctx.SetConditionFromResult(status.MongoDBAtlasConnectionReadyType, result)
		return ctrl.Result{}, false
	}
	return ctrl.Result{}, true
}

// deleteDBUserFromAtlas delete database user from Atlas
func (r *MongoDBAtlasConnectionReconciler) deleteDBUserFromAtlas(conn *dbaas.MongoDBAtlasConnection, projectID, dbUserName string, inventory *dbaas.MongoDBAtlasInventory, log *zap.SugaredLogger) error {
	atlasConnection, err := atlas.ReadConnection(log, r.Client, r.GlobalAPISecret, inventory.ConnectionSecretObjectKey())
	if err != nil {
		return err
	}

	atlasClient, err := atlas.Client(r.AtlasDomain, atlasConnection, log)
	if err != nil {
		return fmt.Errorf("cannot build Atlas client: %w", err)
	}

	go func() {
		timeout := time.Now().Add(workflow.DefaultTimeout)
		for time.Now().Before(timeout) {
			_, err = atlasClient.DatabaseUsers.Delete(context.Background(), "admin", projectID, dbUserName)
			var apiError *mongodbatlas.ErrorResponse
			if errors.As(err, &apiError) && apiError.ErrorCode == atlas.ClusterNotFound {
				log.Info("Cluster doesn't exist or is already deleted")
				return
			}

			if err != nil {
				log.Errorw("Cannot delete Atlas cluster", "error", err)
				time.Sleep(workflow.DefaultRetry)
				continue
			}

			log.Info("Started Atlas cluster deletion process")
			return
		}

		log.Error("Failed to delete Atlas cluster in time")
	}()
	return nil
}

// getOwnedSecret returns a secret object for database credentials with ownership set
func getOwnedSecret(connection *dbaas.MongoDBAtlasConnection, username, password string) *corev1.Secret {
	return &corev1.Secret{
		TypeMeta: metav1.TypeMeta{
			Kind: "Opaque",
		},
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "atlas-db-user-",
			Namespace:    connection.Namespace,
			Labels: map[string]string{
				"managed-by":      "atlas-operator",
				"owner":           connection.Name,
				"owner.kind":      connection.Kind,
				"owner.namespace": connection.Namespace,
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					UID:                connection.GetUID(),
					APIVersion:         "dbaas.redhat.com/v1alpha1",
					BlockOwnerDeletion: ptr.BoolPtr(false),
					Controller:         ptr.BoolPtr(true),
					Kind:               "MongoDBAtlasConnection",
					Name:               connection.Name,
				},
			},
		},
		Data: map[string][]byte{
			DBUserNameKey: []byte(username),
			DBPasswordKey: []byte(password),
		},
	}
}

// getResource queries the Custom Resource 'request.NamespacedName' and populates the 'resource' pointer.
func getResource(client client.Client, namespace, name string, resource mdbv1.AtlasCustomResource, log *zap.SugaredLogger) workflow.Result {
	obj := types.NamespacedName{Namespace: namespace, Name: name}
	err := client.Get(context.Background(), obj, resource)
	if err != nil {
		if apiErrors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Return and don't requeue
			log.Debugf("Object %s doesn't exist, was it deleted after reconcile request?", obj)
			return workflow.TerminateSilently().WithoutRetry()
		}
		// Error reading the object - requeue the request. Note, that we don't intend to update resource status
		// as most of all it will fail as well.
		log.Errorf("Failed to query object %s: %s", obj, err)
		return workflow.TerminateSilently()
	}

	return workflow.OK()
}

// isReadyForBinding is the MongoDBAtlasConnection ready for binding?
func isReadyForBinding(connection *dbaas.MongoDBAtlasConnection) bool {
	cond := connection.Status.GetCondition(status.MongoDBAtlasConnectionReadyType)
	return cond != nil && cond.Status == corev1.ConditionTrue
}

// isInventoryReady is the MongoDBAtlasInvenotry ready?
func isInventoryReady(connection *dbaas.MongoDBAtlasInventory) bool {
	cond := connection.Status.GetCondition(status.ReadyType)
	return cond != nil && cond.Status == corev1.ConditionTrue
}

// getInstance returns an instance from the inventory based on instanceID
func getInstance(inventory *dbaas.MongoDBAtlasInventory, instanceID string) *dbaas.Instance {
	for _, instance := range inventory.Status.Instances {
		if instance.InstanceID == instanceID {
			//Found the instance based on its ID
			return &instance
		}
	}
	return nil
}

// getHost returns the database connection host
func getHost(instance *dbaas.Instance) string {
	connectionHost := ""
	connStrTokens := strings.Split(instance.InstanceInfo.ConnectionString, "://")
	if len(connStrTokens) < 2 {
		//There is no "://" found
		connectionHost = connStrTokens[0]
	} else {
		connectionHost = connStrTokens[1]
	}
	return connectionHost
}

// generatePassword generates a random password with at least one digit and one special character.
func generatePassword() string {
	rand.Seed(time.Now().UnixNano())
	digits := "0123456789"
	specials := "~=+%^*/()[]{}/!@#$?|"
	all := "ABCDEFGHIJKLMNOPQRSTUVWXYZ" +
		"abcdefghijklmnopqrstuvwxyz" +
		digits + specials
	length := 8
	buf := make([]byte, length)
	buf[0] = digits[rand.Intn(len(digits))]
	buf[1] = specials[rand.Intn(len(specials))]
	for i := 2; i < length; i++ {
		buf[i] = all[rand.Intn(len(all))]
	}
	rand.Shuffle(len(buf), func(i, j int) {
		buf[i], buf[j] = buf[j], buf[i]
	})
	return string(buf) // E.g. "3i[g0|)z"
}
