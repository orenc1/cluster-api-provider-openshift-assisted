package assistedinstaller

import (
	"github.com/openshift-assisted/cluster-api-agent/controlplane/api/v1alpha1"
	"github.com/openshift-assisted/cluster-api-agent/util"
	hivev1 "github.com/openshift/hive/apis/hive/v1"
	"github.com/openshift/hive/apis/hive/v1/agent"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func GetClusterDeploymentFromConfig(acp *v1alpha1.AgentControlPlane, clusterName string) *hivev1.ClusterDeployment {
	// Get cluster clusterName instead of reference to ACP clusterName
	clusterDeployment := &hivev1.ClusterDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      acp.Name,
			Namespace: acp.Namespace,
			Labels:    util.ControlPlaneMachineLabelsForCluster(acp, clusterName),
		},
		Spec: hivev1.ClusterDeploymentSpec{
			ClusterName: clusterName,
			BaseDomain:  acp.Spec.AgentConfigSpec.BaseDomain,
			Platform: hivev1.Platform{
				AgentBareMetal: &agent.BareMetalPlatform{
					//AgentSelector: metav1.LabelSelector{}, // TODO: What label should we select?
				},
			},
			//PreserveOnDelete: True, // TODO take from CR
			//ControlPlaneConfig: hivev1.ControlPlaneConfigSpec{}, //
			//Ingress: ( []hivev1.ClusterIngress)
			//CertificateBundles: ([]hivev1.CertificateBundleSpec)
			// ManageDNS: bool,
			//ClusterMetadata: *hivev1.ClusterMetadata.
			Installed: false,
			// Provisioning: *hivev1.Provisioning
			//ClusterInstallRef: // reference to AgentClusterInstall  *ClusterInstallLocalReference
			// ClusterPoolRef *ClusterPoolReference `json:"clusterPoolRef,omitempty"`,
			// ClusterPoolRef *ClusterPoolReference `json:"clusterPoolRef,omitempty"`
			PullSecretRef: acp.Spec.AgentConfigSpec.PullSecretRef,
		},
	}
	return clusterDeployment
}
