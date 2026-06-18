/*
Copyright 2024.

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

package controller

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	controlplanev1alpha3 "github.com/openshift-assisted/cluster-api-provider-openshift-assisted/controlplane/api/v1alpha3"
	"github.com/openshift-assisted/cluster-api-provider-openshift-assisted/util"
	logutil "github.com/openshift-assisted/cluster-api-provider-openshift-assisted/util/log"
	hiveext "github.com/openshift/assisted-service/api/hiveextension/v1beta1"
	aiv1beta1 "github.com/openshift/assisted-service/api/v1beta1"
	aimodels "github.com/openshift/assisted-service/models"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	capiutil "sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/cluster-api/util/collections"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	kubeconfigSecretKey = "kubeconfig"

	// InstallationRetryAnnotation tracks the number of automatic installation retries.
	InstallationRetryAnnotation = "controlplane.cluster.x-k8s.io/installation-retry-count"
	// MaxInstallationRetries is the maximum number of auto-retries before giving up.
	MaxInstallationRetries = 3
)

// AgentClusterInstallReconciler reconciles a AgentClusterInstall object
type AgentClusterInstallReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// SetupWithManager sets up the controller with the Manager.
func (r *AgentClusterInstallReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&hiveext.AgentClusterInstall{}).
		Complete(r)
}

// +kubebuilder:rbac:groups=extensions.hive.openshift.io,resources=agentclusterinstalls,verbs=get;list;watch;delete
// +kubebuilder:rbac:groups=extensions.hive.openshift.io,resources=agentclusterinstalls/status,verbs=get
// +kubebuilder:rbac:groups=controlplane.cluster.x-k8s.io,resources=openshiftassistedcontrolplanes,verbs=get;list;watch;update
// +kubebuilder:rbac:groups=controlplane.cluster.x-k8s.io,resources=openshiftassistedcontrolplanes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update
// +kubebuilder:rbac:groups=agent-install.openshift.io,resources=infraenvs,verbs=get;list;watch;delete
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=machines,verbs=get;list;watch;delete
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters,verbs=get;list;watch

func (r *AgentClusterInstallReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)

	defer func() {
		log.V(logutil.DebugLevel).Info("agent cluster install reconcile ended")
	}()

	log.V(logutil.DebugLevel).Info("agent cluster install reconcile started")
	aci := &hiveext.AgentClusterInstall{}
	if err := r.Get(ctx, req.NamespacedName, aci); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	log.WithValues("agent_cluster_install", aci.Name, "agent_cluster_install_namespace", aci.Namespace)

	oacp := controlplanev1alpha3.OpenshiftAssistedControlPlane{}
	if err := util.GetTypedOwner(ctx, r.Client, aci, &oacp); err != nil {
		return ctrl.Result{}, err
	}
	log.WithValues("openshiftassisted_control_plane", oacp.Name, "openshiftassisted_control_plane_namespace", oacp.Namespace)

	// Auto-retry: if the installation has failed, reset and retry (KubeVirt platform only).
	if isInstallationFailed(aci) {
		if result, err := r.handleInstallationFailure(ctx, aci, &oacp); err != nil || !result.IsZero() {
			return result, err
		}
	}

	if err := r.reconcile(ctx, aci, &oacp); err != nil {
		return ctrl.Result{}, err
	}

	// Check if AgentClusterInstall has reached finalizing or day 2 (adding-hosts) state
	if isAvailable(aci) {
		oacp.Status.Initialization.ControlPlaneInitialized = ptr.To(true)
		setConditionTrue(&oacp, controlplanev1alpha3.ControlPlaneAvailableCondition)
		return ctrl.Result{}, r.updateControlplaneStatus(ctx, &oacp)
	}
	setConditionFalse(&oacp, controlplanev1alpha3.ControlPlaneAvailableCondition,
		controlplanev1alpha3.ControlPlaneInstallingReason,
		"Controlplane installing, status: %s", aci.Status.DebugInfo.State)
	return ctrl.Result{}, r.updateControlplaneStatus(ctx, &oacp)
}

func (r *AgentClusterInstallReconciler) reconcile(
	ctx context.Context,
	aci *hiveext.AgentClusterInstall,
	oacp *controlplanev1alpha3.OpenshiftAssistedControlPlane,
) error {
	if !hasKubeconfigRef(aci) {
		setConditionFalse(oacp, controlplanev1alpha3.KubeconfigAvailableCondition,
			controlplanev1alpha3.KubeconfigUnavailableFailedReason, "Kubeconfig not available")
		return nil
	}

	kubeconfigSecret, err := r.getACIKubeconfig(ctx, aci, *oacp)
	if err != nil {
		setConditionFalse(oacp, controlplanev1alpha3.KubeconfigAvailableCondition,
			controlplanev1alpha3.KubeconfigUnavailableFailedReason,
			"error retrieving Kubeconfig %v", err)
		return err
	}

	clusterName := oacp.Labels[clusterv1.ClusterNameLabel]
	labels := map[string]string{
		clusterv1.ClusterNameLabel: clusterName,
	}

	if err := r.updateLabels(ctx, kubeconfigSecret, labels); err != nil {
		setConditionFalse(oacp, controlplanev1alpha3.KubeconfigAvailableCondition,
			controlplanev1alpha3.KubeconfigUnavailableFailedReason,
			"error updating Kubeconfig secret labels %v", err)
		return err
	}

	if !r.ClusterKubeconfigSecretExists(ctx, clusterName, oacp.Namespace) {
		if err := r.createKubeconfig(ctx, kubeconfigSecret, clusterName, *oacp); err != nil {
			setConditionFalse(oacp, controlplanev1alpha3.KubeconfigAvailableCondition,
				controlplanev1alpha3.KubeconfigUnavailableFailedReason,
				"error creating Kubeconfig secret: %v", err)
			return err
		}
	}
	setConditionTrue(oacp, controlplanev1alpha3.KubeconfigAvailableCondition)

	oacp.Status.Initialization.ControlPlaneInitialized = ptr.To(true)
	if err := r.Client.Status().Update(ctx, oacp); err != nil {
		return err
	}
	return nil
}

func (r *AgentClusterInstallReconciler) createKubeconfig(
	ctx context.Context,
	kubeconfigSecret *corev1.Secret,
	clusterName string,
	acp controlplanev1alpha3.OpenshiftAssistedControlPlane,
) error {
	kubeconfig, ok := kubeconfigSecret.Data[kubeconfigSecretKey]
	if !ok {
		return fmt.Errorf("kubeconfig with key `%s` not found in secret %s", kubeconfigSecretKey, kubeconfigSecret.Name)
	}

	// When using Route-based access (pod networking), rewrite the kubeconfig server URL
	// from port 6443 to port 443 so it goes through the infra router's passthrough Route.
	// The hostname stays the same (api.<cluster>.<baseDomain>), preserving TLS validity.
	if acp.Spec.Config.Platform == controlplanev1alpha3.PlatformKubeVirt &&
		acp.Spec.Config.KubeVirt != nil &&
		acp.Spec.Config.KubeVirt.ExternalAccess != nil &&
		acp.Spec.Config.KubeVirt.ExternalAccess.UseRoutes {
		kubeconfig = rewriteKubeconfigPort(kubeconfig)
	}

	// Create secret <cluster-name>-kubeconfig from original kubeconfig secret - this is what the CAPI Cluster looks for to set the control plane as initialized
	clusterNameKubeconfigSecret := GenerateSecretWithOwner(
		client.ObjectKey{Name: clusterName, Namespace: acp.Namespace},
		kubeconfig,
		*metav1.NewControllerRef(&acp, controlplanev1alpha3.GroupVersion.WithKind(openshiftAssistedControlPlaneKind)),
	)
	if err := r.Create(ctx, clusterNameKubeconfigSecret); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return err
		}
		if err := r.Update(ctx, clusterNameKubeconfigSecret); err != nil {
			return err
		}
	}
	return nil
}

// rewriteKubeconfigPort replaces :6443 with :443 in the kubeconfig server URL.
// This allows the kubeconfig to work through the infra cluster's OpenShift Router
// (passthrough Route on port 443) while preserving the hostname for TLS validation.
func rewriteKubeconfigPort(kubeconfig []byte) []byte {
	content := string(kubeconfig)
	content = strings.ReplaceAll(content, ":6443", ":443")
	return []byte(content)
}

func (r *AgentClusterInstallReconciler) updateLabels(
	ctx context.Context,
	obj client.Object,
	labels map[string]string,
) error {
	objLabels := obj.GetLabels()
	if len(objLabels) < 1 {
		objLabels = make(map[string]string)
	}

	for k, v := range labels {
		objLabels[k] = v
	}
	obj.SetLabels(objLabels)
	if err := r.Update(ctx, obj); err != nil {
		return err
	}
	return nil
}

func (r *AgentClusterInstallReconciler) getACIKubeconfig(
	ctx context.Context,
	aci *hiveext.AgentClusterInstall,
	openshiftAssistedCP controlplanev1alpha3.OpenshiftAssistedControlPlane,
) (*corev1.Secret, error) {
	secretName := aci.Spec.ClusterMetadata.AdminKubeconfigSecretRef.Name

	// Get the kubeconfig secret and label with capi key pair cluster.x-k8s.io/cluster-name=<cluster name>
	kubeconfigSecret := &corev1.Secret{}
	if err := r.Get(ctx, client.ObjectKey{Name: secretName, Namespace: openshiftAssistedCP.Namespace}, kubeconfigSecret); err != nil {
		return nil, err
	}
	return kubeconfigSecret, nil
}

func hasKubeconfigRef(aci *hiveext.AgentClusterInstall) bool {
	return aci.Spec.ClusterMetadata != nil && aci.Spec.ClusterMetadata.AdminKubeconfigSecretRef.Name != ""
}

func isAvailable(aci *hiveext.AgentClusterInstall) bool {
	state := aci.Status.DebugInfo.State
	return state == aimodels.ClusterStatusFinalizing || state == aimodels.ClusterStatusAddingHosts
}

func (r *AgentClusterInstallReconciler) ClusterKubeconfigSecretExists(
	ctx context.Context,
	clusterName, namespace string,
) bool {
	secretName := fmt.Sprintf("%s-kubeconfig", clusterName)
	kubeconfigSecret := &corev1.Secret{}
	if err := r.Get(ctx, client.ObjectKey{Name: secretName, Namespace: namespace}, kubeconfigSecret); err != nil {
		return !apierrors.IsNotFound(err)
	}
	return true
}

func (r *AgentClusterInstallReconciler) updateControlplaneStatus(ctx context.Context, oacp *controlplanev1alpha3.OpenshiftAssistedControlPlane) error {
	if err := r.Client.Status().Update(ctx, oacp); err != nil {
		return err
	}
	return nil
}

func isInstallationFailed(aci *hiveext.AgentClusterInstall) bool {
	return aci.Status.DebugInfo.State == aimodels.ClusterStatusError
}

// handleInstallationFailure implements auto-retry for failed installations on KubeVirt platform.
// When installation fails (typically due to transient network issues), it resets the cluster
// by deleting the ACI, InfraEnvs, and Machines. The existing controllers recreate everything
// automatically, giving the installation a fresh attempt.
func (r *AgentClusterInstallReconciler) handleInstallationFailure(
	ctx context.Context,
	aci *hiveext.AgentClusterInstall,
	oacp *controlplanev1alpha3.OpenshiftAssistedControlPlane,
) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)

	// Only auto-retry for KubeVirt platform clusters
	if oacp.Spec.Config.Platform != controlplanev1alpha3.PlatformKubeVirt {
		log.Info("installation failed but auto-retry is only supported for KubeVirt platform")
		return ctrl.Result{}, nil
	}

	retryCount := getRetryCount(oacp)
	if retryCount >= MaxInstallationRetries {
		log.Info("installation failed and max retries exceeded",
			"retryCount", retryCount, "maxRetries", MaxInstallationRetries)
		setConditionFalse(oacp, controlplanev1alpha3.ControlPlaneAvailableCondition,
			"InstallationFailedMaxRetries",
			"Installation failed after %d retries: %s", retryCount, aci.Status.DebugInfo.StateInfo)
		return ctrl.Result{}, r.updateControlplaneStatus(ctx, oacp)
	}

	log.Info("installation failed, initiating automatic retry",
		"retryCount", retryCount+1, "maxRetries", MaxInstallationRetries,
		"reason", aci.Status.DebugInfo.StateInfo)

	// Increment retry counter
	if err := r.setRetryCount(ctx, oacp, retryCount+1); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to update retry count: %w", err)
	}

	// Delete all InfraEnvs in the namespace (they'll be recreated by the bootstrap controller)
	infraEnvList := &aiv1beta1.InfraEnvList{}
	if err := r.List(ctx, infraEnvList, client.InNamespace(oacp.Namespace)); err == nil {
		for i := range infraEnvList.Items {
			if err := r.Delete(ctx, &infraEnvList.Items[i]); err != nil && !apierrors.IsNotFound(err) {
				log.Error(err, "failed to delete InfraEnv", "name", infraEnvList.Items[i].Name)
			}
		}
	}

	// Delete ALL Machines in the cluster (both CP and workers get fresh disks)
	cluster, err := capiutil.GetOwnerCluster(ctx, r.Client, oacp.ObjectMeta)
	if err == nil && cluster != nil {
		allMachines, err := collections.GetFilteredMachinesForCluster(ctx, r.Client, cluster)
		if err == nil {
			for _, machine := range allMachines {
				log.Info("deleting machine for retry", "machine", machine.Name)
				if err := r.Delete(ctx, machine); err != nil && !apierrors.IsNotFound(err) {
					log.Error(err, "failed to delete machine", "name", machine.Name)
				}
			}
		}
	}

	// Delete the ACI itself — the ClusterDeployment controller will recreate it,
	// which re-registers the cluster with the assisted-service backend.
	log.Info("deleting AgentClusterInstall to trigger re-registration")
	if err := r.Delete(ctx, aci); err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, fmt.Errorf("failed to delete ACI for retry: %w", err)
	}

	// Requeue after a delay to allow cleanup to propagate
	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

func getRetryCount(oacp *controlplanev1alpha3.OpenshiftAssistedControlPlane) int {
	if oacp.Annotations == nil {
		return 0
	}
	countStr, ok := oacp.Annotations[InstallationRetryAnnotation]
	if !ok {
		return 0
	}
	count, err := strconv.Atoi(countStr)
	if err != nil {
		return 0
	}
	return count
}

func (r *AgentClusterInstallReconciler) setRetryCount(ctx context.Context, oacp *controlplanev1alpha3.OpenshiftAssistedControlPlane, count int) error {
	if oacp.Annotations == nil {
		oacp.Annotations = make(map[string]string)
	}
	oacp.Annotations[InstallationRetryAnnotation] = strconv.Itoa(count)
	return r.Update(ctx, oacp)
}

// GenerateSecretWithOwner returns a Kubernetes secret for the given Cluster name, namespace, kubeconfig data, and ownerReference.
func GenerateSecretWithOwner(clusterName client.ObjectKey, data []byte, owner metav1.OwnerReference) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-kubeconfig", clusterName.Name),
			Namespace: clusterName.Namespace,
			Labels: map[string]string{
				clusterv1.ClusterNameLabel: clusterName.Name,
			},
			OwnerReferences: []metav1.OwnerReference{
				owner,
			},
		},
		Data: map[string][]byte{
			"value": data,
		},
		Type: clusterv1.ClusterSecretType,
	}
}
