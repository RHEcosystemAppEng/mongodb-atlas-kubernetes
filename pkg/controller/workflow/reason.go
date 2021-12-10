package workflow

type ConditionReason string

// TODO move 'ConditionReason' to 'api' package?

// General reasons
const (
	AtlasCredentialsNotProvided ConditionReason = "AtlasCredentialsNotProvided"
	Internal                    ConditionReason = "InternalError"
)

// Atlas Project reasons
const (
	ProjectNotCreatedInAtlas   ConditionReason = "ProjectNotCreatedInAtlas"
	ProjectIPAccessInvalid     ConditionReason = "ProjectIPAccessListInvalid"
	ProjectIPNotCreatedInAtlas ConditionReason = "ProjectIPAccessListNotCreatedInAtlas"
)

// Atlas Cluster reasons
const (
	ClusterNotCreatedInAtlas           ConditionReason = "ClusterNotCreatedInAtlas"
	ClusterNotUpdatedInAtlas           ConditionReason = "ClusterNotUpdatedInAtlas"
	ClusterCreating                    ConditionReason = "ClusterCreating"
	ClusterUpdating                    ConditionReason = "ClusterUpdating"
	ClusterDeleting                    ConditionReason = "ClusterDeleting"
	ClusterDeleted                     ConditionReason = "ClusterDeleted"
	ClusterConnectionSecretsNotCreated ConditionReason = "ClusterConnectionSecretsNotCreated"
)

// Atlas Database User reasons
const (
	DatabaseUserNotCreatedInAtlas           ConditionReason = "DatabaseUserNotCreatedInAtlas"
	DatabaseUserNotUpdatedInAtlas           ConditionReason = "DatabaseUserNotUpdatedInAtlas"
	DatabaseUserConnectionSecretsNotCreated ConditionReason = "DatabaseUserConnectionSecretsNotCreated"
	DatabaseUserStaleConnectionSecrets      ConditionReason = "DatabaseUserStaleConnectionSecrets"
	DatabaseUserClustersAppliedChanges      ConditionReason = "ClustersAppliedDatabaseUsersChanges"
	DatabaseUserInvalidSpec                 ConditionReason = "DatabaseUserInvalidSpec"
	DatabaseUserExpired                     ConditionReason = "DatabaseUserExpired"
)

// MongoDBAtlasInventory reasons
const (
	MongoDBAtlasInventorySyncOK              ConditionReason = "SyncOK"
	MongoDBAtlasInventoryInputError          ConditionReason = "InputError"
	MongoDBAtlasInventoryBackendError        ConditionReason = "BackendError"
	MongoDBAtlasInventoryEndpointUnreachable ConditionReason = "EndpointUnreachable"
	MongoDBAtlasInventoryAuthenticationError ConditionReason = "AuthenticationError"
)

// MongoDBAtlasConnection reasons
const (
	MongoDBAtlasConnectionReady               ConditionReason = "Ready"
	MongoDBAtlasConnectionAtlasUnreachable    ConditionReason = "Unreachable"
	MongoDBAtlasConnectionInventoryNotReady   ConditionReason = "InventoryNotReady"
	MongoDBAtlasConnectionInventoryNotFound   ConditionReason = "InventoryNotFound"
	MongoDBAtlasConnectionInstanceIDNotFound  ConditionReason = "InstanceIDNotFound"
	MongoDBAtlasConnectionBackendError        ConditionReason = "BackendError"
	MongoDBAtlasConnectionAuthenticationError ConditionReason = "AuthenticationError"
	MongoDBAtlasConnectionInprogress          ConditionReason = "Inprogress"
)

// MongoDBAtlasInstance reasons
const (
	MongoDBAtlasInstanceReady                   ConditionReason = "Ready"
	MongoDBAtlasInstanceAtlasUnreachable        ConditionReason = "Unreachable"
	MongoDBAtlasInstanceInventoryNotFound       ConditionReason = "InventoryNotFound"
	MongoDBAtlasInstanceParamsConfigMapNotFound ConditionReason = "ParamsConfigMapNotFound"
	MongoDBAtlasInstanceBackendError            ConditionReason = "BackendError"
	MongoDBAtlasInstanceAuthenticationError     ConditionReason = "AuthenticationError"
	MongoDBAtlasInstanceInprogress              ConditionReason = "Inprogress"
)
