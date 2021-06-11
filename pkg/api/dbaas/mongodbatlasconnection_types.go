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
	"github.com/mongodb/mongodb-atlas-kubernetes/pkg/api/v1/status"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// MongoDBAtlasConnectionSpec defines the desired state of MongoDBAtlasConnection
type MongoDBAtlasConnectionSpec struct {
	// Important: Run "make" to regenerate code after modifying this file

	// A reference to the relevant MongoDBAtlasConnection CR
	ConnectionRef *corev1.LocalObjectReference `json:"Connection"`

	// The ID of the instance to connect to, as seen in the Status of
	// the referenced MongoDBAtlasConnection
	InstanceID string `json:"instanceID"`
}

// MongoDBAtlasConnectionStatus defines the observed state of MongoDBAtlasConnection
type MongoDBAtlasConnectionStatus struct {
	status.Common `json:",inline"`

	// The host for this instance
	Host string `json:"host,omitempty"`

	// Secret holding username and password
	CredentialsRef *corev1.LocalObjectReference `json:"credentialsRef,omitempty"`

	// Any other provider-specific information related to this connection
	ConnectionInfo ConnectionInfo `json:"connectionInfo,omitempty"`
}

type ConnectionInfo struct {
	// ConnectionString is for your applications use to connect to this cluster.
	ConnectionString string `json:"connectionString,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +groupName:=dbaas.redhat.com
// +versionName:=v1alpha1

// MongoDBAtlasConnection is the Schema for the MongoDBAtlasConnections API
type MongoDBAtlasConnection struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MongoDBAtlasConnectionSpec   `json:"spec,omitempty"`
	Status MongoDBAtlasConnectionStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// MongoDBAtlasConnectionList contains a list of MongoDBAtlasConnection
type MongoDBAtlasConnectionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MongoDBAtlasConnection `json:"items"`
}

func init() {
	DBaaSSchemeBuilder.Register(&MongoDBAtlasConnection{}, &MongoDBAtlasConnectionList{})
}

// +k8s:deepcopy-gen=false

// MongoDBAtlasConnectionStatusOption is the option that is applied to Atlas Cluster Status.
type MongoDBAtlasConnectionStatusOption func(s *MongoDBAtlasConnectionStatus)

func (p *MongoDBAtlasConnection) GetStatus() status.Status {
	return p.Status
}

func (c *MongoDBAtlasConnection) UpdateStatus(conditions []status.Condition, options ...status.Option) {
	c.Status.Conditions = conditions
	c.Status.ObservedGeneration = c.ObjectMeta.Generation

	for _, o := range options {
		// This will fail if the Option passed is incorrect - which is expected
		v := o.(MongoDBAtlasConnectionStatusOption)
		v(&c.Status)
	}
}

func AtlasConnectionHostOption(host string) MongoDBAtlasConnectionStatusOption {
	return func(s *MongoDBAtlasConnectionStatus) {
		s.Host = host
	}
}

func AtlasConnectionCredentialsRefOption(secretName string) MongoDBAtlasConnectionStatusOption {
	return func(s *MongoDBAtlasConnectionStatus) {
		s.CredentialsRef = &corev1.LocalObjectReference{
			Name: secretName,
		}
	}
}

func AtlasConnectionStringOption(connectionString string) MongoDBAtlasConnectionStatusOption {
	return func(s *MongoDBAtlasConnectionStatus) {
		s.ConnectionInfo = ConnectionInfo{
			ConnectionString: connectionString,
		}
	}
}
