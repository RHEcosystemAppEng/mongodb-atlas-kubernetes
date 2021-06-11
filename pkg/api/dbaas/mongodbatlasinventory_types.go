/*
Copyright 2021.
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

package dbaas

import (
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mongodb/mongodb-atlas-kubernetes/pkg/api/v1/status"
	"github.com/mongodb/mongodb-atlas-kubernetes/pkg/util/compat"
	"github.com/mongodb/mongodb-atlas-kubernetes/pkg/util/kube"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// MongoDBAtlasInventorySpec defines the desired state of MongoDBAtlasInventory
type MongoDBAtlasInventorySpec struct {
	// Important: Run "make" to regenerate code after modifying this file

	// The name of the database provider whose instances are being listed. This must match the
	// provider name specified in the provider’s operator in its registration ConfigMap.
	Provider DatabaseProvider `json:"provider"`

	// The properties that will be copied into the provider’s inventory Spec
	MongoDBAtlasInventoryProviderSpec `json:"providerSpec"`
}

// MongoDBAtlasInventoryProviderSpec The Inventory Spec to be used by provider operators
type MongoDBAtlasInventoryProviderSpec struct {
	// The Secret containing the provider-specific connection credentials to use with its API
	// endpoint.  The format of the Secret is specified in the provider’s operator in its registration
	// ConfigMap (credentials_fields key).  It is recommended to place the Secret in a namespace
	// with limited accessibility.
	CredentialsRef *NamespacedName `json:"credentialsRef"`
}

type NamespacedName struct {
	// The namespace where object of known type is stored
	Namespace string `json:"namespace,omitempty"`

	// The name for object of known type
	Name string `json:"name,omitempty"`
}

type DatabaseProvider struct {
	Name string `json:"name"`
}

// MongoDBAtlasInventoryStatus defines the observed state of MongoDBAtlasInventory
type MongoDBAtlasInventoryStatus struct {
	status.Common `json:",inline"`

	// Type used by the Service Binding Operator - e.g., MongoDB, Postgres
	ServiceBindingType string `json:"type"`

	// Provider used by the Service Binding Operator
	ServiceBindingProvider string `json:"provider"`

	// A list of instances returned from querying the DB provider
	Instances []Instance `json:"instances,omitempty"`
}

type Instance struct {
	// A provider-specific identifier for this instance in the database service.  It may contain one or
	// more pieces of information used by the provider operator to identify the instance on the
	// database service.
	InstanceID string `json:"instanceID"`

	// The name of this instance in the database service
	Name string `json:"name,omitempty"`

	// Any other provider-specific information related to this instance
	InstanceInfo InstanceInfo `json:"extraInfo,omitempty"`
}

type InstanceInfo struct {
	CloudProvider    string `json:"providerName"`
	CloudRegion      string `json:"regionName"`
	ProjectID        string `json:"projectID"`
	ProjectName      string `json:"projectName"`
	InstanceSizeName string `json:"instanceSizeName"`
	// ConnectionString is for your applications use to connect to this cluster.
	ConnectionString string `json:"connectionString,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +groupName:=dbaas.redhat.com
// +versionName:=v1alpha1

// MongoDBAtlasInventory is the Schema for the MongoDBAtlasInventory API
type MongoDBAtlasInventory struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MongoDBAtlasInventorySpec   `json:"spec,omitempty"`
	Status MongoDBAtlasInventoryStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// MongoDBAtlasInventoryList contains a list of DBaaSInventories
type MongoDBAtlasInventoryList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MongoDBAtlasInventory `json:"items"`
}

func init() {
	DBaaSSchemeBuilder.Register(&MongoDBAtlasInventory{}, &MongoDBAtlasInventoryList{})
}

func (p *MongoDBAtlasInventory) ConnectionSecretObjectKey() *client.ObjectKey {
	if p.Spec.CredentialsRef != nil {
		key := kube.ObjectKey(p.Spec.CredentialsRef.Namespace, p.Spec.CredentialsRef.Name)
		return &key
	}
	return nil
}

// +k8s:deepcopy-gen=false

// MongoDBAtlasInventoryStatusOption is the option that is applied to Atlas Cluster Status.
type MongoDBAtlasInventoryStatusOption func(s *MongoDBAtlasInventoryStatus)

func MongoDBAtlasInventoryListOption(instanceList []Instance) MongoDBAtlasInventoryStatusOption {
	return func(s *MongoDBAtlasInventoryStatus) {
		sl := []Instance{}
		err := compat.JSONCopy(&sl, instanceList)
		if err != nil {
			return
		}
		s.Instances = sl
	}
}

func (p *MongoDBAtlasInventory) GetStatus() status.Status {
	return p.Status
}

func (p *MongoDBAtlasInventory) UpdateStatus(conditions []status.Condition, options ...status.Option) {
	p.Status.Conditions = conditions
	p.Status.ObservedGeneration = p.ObjectMeta.Generation

	for _, o := range options {
		// This will fail if the Option passed is incorrect - which is expected
		v := o.(MongoDBAtlasInventoryStatusOption)
		v(&p.Status)
	}
}
