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

package kubevirt

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	infraClusterCredentialsSecretName = "infra-cluster-credentials"
	infraClusterKubeconfigKey         = "kubeconfig"
)

// InfraClientResult contains the infra cluster client and the resolved namespace.
type InfraClientResult struct {
	Client    client.Client
	Namespace string
	IsRemote  bool
}

// GetInfraClusterClient builds a controller-runtime client for the infra cluster.
// If the infra-cluster-credentials secret exists in the given namespace, a remote
// client is built from the kubeconfig it contains. Otherwise, the local client is
// returned (same-cluster mode).
func GetInfraClusterClient(
	ctx context.Context,
	localClient client.Client,
	scheme *runtime.Scheme,
	namespace string,
) (*InfraClientResult, error) {
	secret := &corev1.Secret{}
	err := localClient.Get(ctx, client.ObjectKey{
		Name:      infraClusterCredentialsSecretName,
		Namespace: namespace,
	}, secret)
	if err != nil {
		if client.IgnoreNotFound(err) == nil {
			return &InfraClientResult{
				Client:    localClient,
				Namespace: namespace,
				IsRemote:  false,
			}, nil
		}
		return nil, fmt.Errorf("failed to get infra-cluster-credentials secret: %w", err)
	}

	kubeconfig, ok := secret.Data[infraClusterKubeconfigKey]
	if !ok {
		return nil, fmt.Errorf("infra-cluster-credentials secret missing %q key", infraClusterKubeconfigKey)
	}

	clientConfig, err := clientcmd.NewClientConfigFromBytes(kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("failed to parse infra cluster kubeconfig: %w", err)
	}

	infraNamespace := namespace
	if nsBytes, ok := secret.Data["namespace"]; ok {
		infraNamespace = string(nsBytes)
	}

	restConfig, err := clientConfig.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to build infra cluster REST config: %w", err)
	}

	infraClient, err := client.New(restConfig, client.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("failed to create infra cluster client: %w", err)
	}

	return &InfraClientResult{
		Client:    infraClient,
		Namespace: infraNamespace,
		IsRemote:  true,
	}, nil
}

// EnsurePullSecretOnInfra copies the pull secret from the management cluster to the
// infra cluster namespace so that Jobs running on the infra cluster can pull OCP release images.
func EnsurePullSecretOnInfra(
	ctx context.Context,
	localClient client.Client,
	infraClient client.Client,
	localNamespace string,
	infraNamespace string,
	secretName string,
) error {
	if secretName == "" {
		return nil
	}

	// Check if it already exists on infra
	existing := &corev1.Secret{}
	err := infraClient.Get(ctx, client.ObjectKey{Name: secretName, Namespace: infraNamespace}, existing)
	if err == nil {
		return nil
	}
	if client.IgnoreNotFound(err) != nil {
		return err
	}

	// Read from management cluster
	source := &corev1.Secret{}
	if err := localClient.Get(ctx, client.ObjectKey{Name: secretName, Namespace: localNamespace}, source); err != nil {
		return fmt.Errorf("failed to get pull secret %s/%s from management cluster: %w", localNamespace, secretName, err)
	}

	// Create on infra cluster
	target := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: infraNamespace,
			Labels: map[string]string{
				"capoa.openshift.io/managed-by": "capoa-controlplane",
			},
		},
		Type: source.Type,
		Data: source.Data,
	}
	if err := infraClient.Create(ctx, target); err != nil {
		if client.IgnoreAlreadyExists(err) == nil {
			return nil
		}
		return fmt.Errorf("failed to create pull secret on infra cluster: %w", err)
	}
	return nil
}
