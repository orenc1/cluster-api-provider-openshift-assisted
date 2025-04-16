package assistedinstaller

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func GetAssistedHTTPClient(config ServiceConfig, c client.Client) (*http.Client, error) {
	if config.AssistedCABundleName == "" && config.AssistedCABundleNamespace == "" {
		return &http.Client{}, nil
	}
	if config.AssistedCABundleName == "" || config.AssistedCABundleNamespace == "" {
		return nil, errors.New("ASSISTED_CA_BUNDLE_NAME and ASSISTED_CA_BUNDLE_NAMESPACE must either both be set or unset")
	}
	if config.AssistedCABundleResource != "secret" && config.AssistedCABundleResource != "configmap" {
		return nil, errors.New("ASSISTED_CA_BUNDLE_RESOURCE must be either configmap or secret")
	}

	caCertPool, err := x509.SystemCertPool()
	if err != nil {
		return nil, fmt.Errorf("failed to obtain system cert pool: %w", err)
	}

	namespacedName := types.NamespacedName{
		Name:      config.AssistedCABundleName,
		Namespace: config.AssistedCABundleNamespace,
	}
	var bundlePEM []byte

	if strings.EqualFold(config.AssistedCABundleResource, "secret") {
		var secretErr error
		bundlePEM, secretErr = getBundlePEMFromSecret(namespacedName, config.AssistedCABundleKey, c)
		if secretErr != nil {
			return nil, fmt.Errorf("failed to retrieve cert from secret %s: %w", namespacedName, secretErr)
		}
	}

	if strings.EqualFold(config.AssistedCABundleResource, "configmap") {
		var configmapErr error
		bundlePEM, configmapErr = getBundlePEMFromConfigmap(namespacedName, config.AssistedCABundleKey, c)
		if configmapErr != nil {
			return nil, fmt.Errorf("failed to retrieve cert from configmap %s: %w", namespacedName, configmapErr)
		}
	}
	if bundlePEM == nil || !caCertPool.AppendCertsFromPEM(bundlePEM) {
		return nil, fmt.Errorf("failed to append additional certificates")
	}

	defaultTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, fmt.Errorf("expected http.DefaultTransport to be of type *http.Transport")
	}
	transport := defaultTransport.Clone()
	transport.TLSClientConfig = &tls.Config{
		RootCAs: caCertPool,
	}

	return &http.Client{Transport: transport}, nil
}

func getBundlePEMFromConfigmap(namespacedName types.NamespacedName, certKey string, c client.Client) ([]byte, error) {
	configmap := &corev1.ConfigMap{}

	if err := c.Get(context.Background(), namespacedName, configmap); err != nil {
		return nil, fmt.Errorf("failed to get configmap %s: %w", namespacedName, err)
	}

	bundlePEM, present := configmap.Data[certKey]
	if !present {
		return nil, fmt.Errorf("key %s not found in configmap %s", certKey, namespacedName)
	}
	return []byte(bundlePEM), nil
}

func getBundlePEMFromSecret(namespacedName types.NamespacedName, certKey string, c client.Client) ([]byte, error) {
	secret := &corev1.Secret{}

	if err := c.Get(context.Background(), namespacedName, secret); err != nil {
		return nil, fmt.Errorf("failed to get secret %s: %w", namespacedName, err)
	}

	bundlePEMBytes, present := secret.Data[certKey]
	if !present {
		return nil, fmt.Errorf("key %s not found in secret %s", certKey, namespacedName)
	}
	return bundlePEMBytes, nil
}
