package assistedinstaller

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func GetAssistedHTTPClient(config ServiceConfig, c client.Client) (*http.Client, error) {
	if config.AssistedCABundleName == "" && config.AssistedCABundleNamespace == "" {
		return &http.Client{}, nil
	}
	if config.AssistedCABundleName == "" || config.AssistedCABundleNamespace == "" {
		return nil, fmt.Errorf("ASSISTED_CA_BUNDLE_NAME and ASSISTED_CA_BUNDLE_NAMESPACE must either both be set or unset")
	}

	cmNSName := types.NamespacedName{
		Name:      config.AssistedCABundleName,
		Namespace: config.AssistedCABundleNamespace,
	}
	cm := &corev1.ConfigMap{}
	if err := c.Get(context.Background(), cmNSName, cm); err != nil {
		return nil, err
	}
	bundlePEM, present := cm.Data[config.AssistedCABundleKey]
	if !present {
		return nil, fmt.Errorf("key %s not found in configmap %s", config.AssistedCABundleKey, cmNSName)
	}

	caCertPool, err := x509.SystemCertPool()
	if err != nil {
		return nil, fmt.Errorf("failed to obtain system cert pool: %w", err)
	}
	if !caCertPool.AppendCertsFromPEM([]byte(bundlePEM)) {
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
