package upgrade

import (
	"context"
	"errors"
	"fmt"

	controlplanev1alpha2 "github.com/openshift-assisted/cluster-api-agent/controlplane/api/v1alpha2"
	"github.com/openshift-assisted/cluster-api-agent/controlplane/internal/workloadclient"
	"github.com/openshift-assisted/cluster-api-agent/util"

	configv1 "github.com/openshift/api/config/v1"

	"github.com/coreos/go-semver/semver"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func IsUpgradeRequested(ctx context.Context, oacp *controlplanev1alpha2.OpenshiftAssistedControlPlane) bool {
	log := ctrl.LoggerFrom(ctx)
	oacpDistVersion, err := semver.NewVersion(oacp.Spec.DistributionVersion)
	if err != nil {
		log.Error(err, "failed to detect OpenShift version from ACP spec", "version", oacp.Spec.DistributionVersion)
		return false
	}

	upgrade := false
	if oacp.Status.DistributionVersion == "" {
		return false
	}
	currentOACPDistVersion, err := semver.NewVersion(oacp.Status.DistributionVersion)
	if err != nil {
		log.Error(err, "failed to detect OpenShift version from ACP status", "version", oacp.Spec.DistributionVersion)
		return false
	}

	if oacpDistVersion.Compare(*currentOACPDistVersion) > 0 {
		log.Info("Upgrade detected, new requested version is greater than current version",
			"new requested version", oacpDistVersion.String(), "current version", currentOACPDistVersion.String())
		upgrade = true
	}
	return upgrade
}

func GetWorkloadClusterVersion(ctx context.Context, client client.Client,
	workloadClusterClientGenerator workloadclient.ClientGenerator,
	oacp *controlplanev1alpha2.OpenshiftAssistedControlPlane) (string, error) {
	workloadClient, err := getWorkloadClient(ctx, client, workloadClusterClientGenerator, oacp)
	if err != nil {
		return "", err
	}

	var clusterVersion configv1.ClusterVersion
	if err := workloadClient.Get(ctx, types.NamespacedName{Name: "version"}, &clusterVersion); err != nil {
		err = errors.Join(err, fmt.Errorf(("failed to get ClusterVersion from workload cluster")))
		return "", err
	}

	return clusterVersion.Status.Desired.Version, nil
}

func getWorkloadClient(ctx context.Context, client client.Client,
	workloadClusterClientGenerator workloadclient.ClientGenerator,
	oacp *controlplanev1alpha2.OpenshiftAssistedControlPlane) (client.Client, error) {
	if !isKubeconfigAvailable(oacp) {
		return nil, fmt.Errorf("kubeconfig for workload cluster is not available yet")
	}

	kubeconfigSecret, err := util.GetClusterKubeconfigSecret(ctx, client, oacp.Labels[clusterv1.ClusterNameLabel], oacp.Namespace)
	if err != nil {
		err = errors.Join(err, fmt.Errorf("failed to get cluster kubeconfig secret"))
		return nil, err
	}

	if kubeconfigSecret == nil {
		return nil, fmt.Errorf("kubeconfig secret was not found")
	}

	kubeconfig, err := util.ExtractKubeconfigFromSecret(kubeconfigSecret, "value")
	if err != nil {
		err = errors.Join(err, fmt.Errorf("failed to extract kubeconfig from secret %s", kubeconfigSecret.Name))
		return nil, err
	}

	workloadClient, err := workloadClusterClientGenerator.GetWorkloadClusterClient(kubeconfig)
	if err != nil {
		err = errors.Join(err, fmt.Errorf("failed to establish client for workload cluster from kubeconfig"))
		return nil, err
	}
	return workloadClient, nil
}

// isKubeconfigAvailable returns true if the openshift assisted control plane
// condition KubeconfigAvailable is true
func isKubeconfigAvailable(oacp *controlplanev1alpha2.OpenshiftAssistedControlPlane) bool {
	kubeconfigFoundCondition := util.FindStatusCondition(oacp.Status.Conditions, controlplanev1alpha2.KubeconfigAvailableCondition)
	if kubeconfigFoundCondition == nil {
		return false
	}
	return kubeconfigFoundCondition.Status == corev1.ConditionTrue
}
