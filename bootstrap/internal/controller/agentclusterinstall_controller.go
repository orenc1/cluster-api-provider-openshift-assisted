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

	bmh_v1alpha1 "github.com/metal3-io/baremetal-operator/apis/metal3.io/v1alpha1"
	metal3 "github.com/metal3-io/cluster-api-provider-metal3/api/v1beta1"
	"github.com/pkg/errors"

	hiveext "github.com/openshift/assisted-service/api/hiveextension/v1beta1"
	aimodels "github.com/openshift/assisted-service/models"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
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

func (r *AgentClusterInstallReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)

	defer func() {
		log.Info("Agent Cluster Install Reconcile ended")
	}()
	log.Info("Agent Cluster Install Reconcile started")
	agentClusterInstall := &hiveext.AgentClusterInstall{}
	if err := r.Client.Get(ctx, req.NamespacedName, agentClusterInstall); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	log.Info("Reconciling AgentClusterInstall", "name", agentClusterInstall.Name, "namespace", agentClusterInstall.Namespace)

	clusterName, ok := agentClusterInstall.GetLabels()[clusterv1.ClusterNameLabel]
	if !ok {
		log.Info("Ending reconcile for agentcluster install, missing clustername label")
		return ctrl.Result{}, nil
	}
	log.Info("agentclusterinstall", "name", agentClusterInstall.Name, "clustername", clusterName)

	// Get all Metal3 Machines associated with this cluster
	metal3Machines := &metal3.Metal3MachineList{}
	if err := r.Client.List(ctx, metal3Machines); err != nil {
		log.Error(err, "couldn't get metal3machines associated with agentclusterinstall", "aci name", agentClusterInstall.Name)
		return ctrl.Result{}, err
	}
	// Check if cluster has finished installing
	if agentClusterInstall.Status.DebugInfo.State == aimodels.ClusterStatusAddingHosts &&
		agentClusterInstall.Spec.ClusterMetadata.AdminKubeconfigSecretRef.Name != "" {
		// Set metal3 node label on spoke node so that capm3 can set the provider ID
		log.Info("agentcluster install finished installing, setting bmh ID label for spoke nodes")
		if err := r.setSpokesNodeLabel(ctx, metal3Machines, agentClusterInstall); err != nil {
			log.Error(err, "failed setting spokes node label", "agentclusterinstall name", agentClusterInstall.Name)
			return ctrl.Result{}, err
		}
		log.Info("finished setting labels on spoke nodes for agentclusterinstall", "agentclusterinstall name", agentClusterInstall.Name, "metal3 machines length", len(metal3Machines.Items))
	}
	return ctrl.Result{}, nil
}

func getSpokeClient(kubeconfig []byte) (client.Client, error) {
	clientConfig, err := clientcmd.NewClientConfigFromBytes(kubeconfig)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get clientconfig from kubeconfig data")
	}

	restConfig, err := clientConfig.ClientConfig()
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get restconfig for kube client")
	}

	schemes := getKubeClientSchemes()
	targetClient, err := client.New(restConfig, client.Options{Scheme: schemes})
	if err != nil {
		return nil, err
	}
	return targetClient, nil
}

func getKubeClientSchemes() *runtime.Scheme {
	var schemes = runtime.NewScheme()
	utilruntime.Must(scheme.AddToScheme(schemes))
	utilruntime.Must(corev1.AddToScheme(schemes))
	return schemes
}

func (r *AgentClusterInstallReconciler) setSpokesNodeLabel(ctx context.Context, metal3Machines *metal3.Metal3MachineList, aci *hiveext.AgentClusterInstall) error {
	log := log.FromContext(ctx)
	secretName := aci.Spec.ClusterMetadata.AdminKubeconfigSecretRef.Name
	kubeconfigSecret := &corev1.Secret{}
	if err := r.Client.Get(ctx, client.ObjectKey{Name: secretName, Namespace: aci.Namespace}, kubeconfigSecret); err != nil {
		log.Error(err, "failed getting kubeconfig secret for agentclusterinstall", "agent cluster install name", aci.Name, "agent cluster install namespace", aci.Namespace, "secret name", secretName)
		return err
	}
	// Create spoke client from kubeconfig
	targetClient, err := getSpokeClient(kubeconfigSecret.Data["kubeconfig"])
	if err != nil {
		log.Error(err, "couldn't create spoke cluster client")
		return err
	}

	for _, metal3Machine := range metal3Machines.Items {
		if err := r.setSpokeNodeLabel(ctx, targetClient, &metal3Machine); err != nil {
			log.Error(err, "couldn't set spoke node label for metal3 machine", "machine name", metal3Machine.Name)
			continue
		}
		log.Info("set label on spoke node", "agent cluster install name", aci.Name, "machine name", metal3Machine.Name)
	}
	return nil
}

func (r *AgentClusterInstallReconciler) setSpokeNodeLabel(ctx context.Context, spokeClient client.Client, metal3Machine *metal3.Metal3Machine) error {
	log := log.FromContext(ctx)
	// Get node name from network
	var nodeName string
	for _, addr := range metal3Machine.Status.Addresses {
		if corev1.NodeAddressType(addr.Type) == corev1.NodeInternalDNS {
			nodeName = addr.Address
			break
		}
	}
	if nodeName != "" {
		node := &corev1.Node{}
		if err := spokeClient.Get(ctx, client.ObjectKey{Name: nodeName}, node); err != nil {
			log.Error(err, "Couldn't get spoke node to set provider id", "node name", nodeName)
			return err
		}
		nodeLabels := node.GetLabels()
		// at this point, the provider id on metal3machine does not exist so we need to label the spoke cluster node
		// with the metal3.io/uuid = bmhID
		// node.Spec.ProviderID = *metal3Machine.Spec.ProviderID
		if nodeLabels == nil {
			nodeLabels = map[string]string{}
		}
		annotations := metal3Machine.ObjectMeta.GetAnnotations()
		if annotations == nil {
			log.Info("metal3machine has no annotations")
			return nil
		}
		hostKey, ok := annotations["metal3.io/BareMetalHost"]
		if !ok {
			log.Info("metal3machine has no BMH annotation")
			return nil
		}
		hostNamespace, hostName, err := cache.SplitMetaNamespaceKey(hostKey)
		if err != nil {
			log.Error(err, "Error parsing annotation value", "annotation key", hostKey)
			return err
		}

		bmh := &bmh_v1alpha1.BareMetalHost{}
		key := client.ObjectKey{
			Name:      hostName,
			Namespace: hostNamespace,
		}
		if err := r.Client.Get(ctx, key, bmh); err != nil {
			log.Error(err, "couldn't get associated bmh", "annotation key", hostKey)
			return err
		}

		bmhID := string(bmh.ObjectMeta.GetUID())

		// Expected node label
		// https://github.com/metal3-io/cluster-api-provider-metal3/blob/1ba01bd229ed1f93065948939202d976b4fae8cc/baremetal/metal3machine_manager.go#L1306
		nodeLabels["metal3.io/uuid"] = bmhID
		if err := spokeClient.Update(ctx, node); err != nil {
			log.Error(err, "Couldn't set provider id on spoke node", "node name", nodeName, "provider id", metal3Machine.Spec.ProviderID)
			return err
		}
		log.Info("added uuid bmh id label to node", "node name", nodeName, "bmhID", bmhID)
	}
	return nil
}
