package failuredomains

import (
	"context"

	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/util/collections"
	"sigs.k8s.io/cluster-api/util/failuredomains"
)

// NextFailureDomainForScaleUp returns the failure domain with the fewest number of machines
func NextFailureDomainForScaleUp(
	ctx context.Context,
	cluster *clusterv1.Cluster,
	machines collections.Machines,
	upToDateMachines collections.Machines,
) (*string, error) {
	failureDomains := FailureDomains(cluster).FilterControlPlane()
	if len(failureDomains) == 0 {
		return nil, nil
	}
	return failuredomains.PickFewest(ctx, failureDomains, machines, upToDateMachines), nil
}

// NextFailureDomainForScaleUp returns the failure domain with the most number of machines
func NextFailureDomainForScaleDown(
	ctx context.Context,
	cluster *clusterv1.Cluster,
	machines collections.Machines,
) (*string, error) {
	failureDomains := FailureDomains(cluster).FilterControlPlane()
	if len(failureDomains) == 0 {
		return nil, nil
	}
	eligibleMachines := machines.Filter(collections.HasAnnotationKey(clusterv1.DeleteMachineAnnotation))
	if eligibleMachines.Len() == 0 {
		eligibleMachines = machines
	}
	return failuredomains.PickMost(ctx, failureDomains, machines, eligibleMachines), nil
}

// FailureDomains returns a slice of failure domain objects synced from the infrastructure provider into Cluster.Status.
func FailureDomains(cluster *clusterv1.Cluster) clusterv1.FailureDomains {
	if cluster == nil || cluster.Status.FailureDomains == nil {
		return clusterv1.FailureDomains{}
	}
	return cluster.Status.FailureDomains
}
