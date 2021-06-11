package atlasinventory

import (
	"context"

	"go.mongodb.org/atlas/mongodbatlas"

	"github.com/mongodb/mongodb-atlas-kubernetes/pkg/api/dbaas"
	"github.com/mongodb/mongodb-atlas-kubernetes/pkg/controller/workflow"
)

// discoverInventories query atlas and return list of inverntories found
func discoverInventories(ctx *workflow.Context) ([]dbaas.Instance, workflow.Result) {
	// Try to find the service
	projects, _, err := ctx.Client.Projects.GetAllProjects(context.Background(), &mongodbatlas.ListOptions{})
	if err != nil {
		return nil, workflow.Terminate(workflow.MongoDBAtlasInventoryProjectListFailed, err.Error())
	}
	instanceList := []dbaas.Instance{}
	for _, p := range projects.Results {
		clusters, _, err := ctx.Client.Clusters.List(context.Background(), p.ID, &mongodbatlas.ListOptions{})
		if err != nil {
			return nil, workflow.Terminate(workflow.MongoDBAtlasInventoryClusterListFailed, err.Error())
		}
		for _, cluster := range clusters {
			clusterSvc := dbaas.Instance{
				InstanceID: cluster.ID,
				Name:       cluster.Name,
				InstanceInfo: dbaas.InstanceInfo{
					InstanceSizeName: cluster.ProviderSettings.InstanceSizeName,
					CloudProvider:    cluster.ProviderSettings.ProviderName,
					CloudRegion:      cluster.ProviderSettings.RegionName,
					ProjectID:        p.ID,
					ProjectName:      p.Name,
					ConnectionString: cluster.ConnectionStrings.StandardSrv,
				},
			}
			instanceList = append(instanceList, clusterSvc)
		}
	}
	return instanceList, workflow.OK()
}
