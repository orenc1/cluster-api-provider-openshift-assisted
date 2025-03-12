package assistedinstaller

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"

	"sigs.k8s.io/controller-runtime/pkg/client"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const testCert = `-----BEGIN CERTIFICATE-----
MIIDZTCCAk2gAwIBAgIUASRIJ1X9QHbJ/+daV+IjQdS1NIowDQYJKoZIhvcNAQEL
BQAwQjELMAkGA1UEBhMCWFgxFTATBgNVBAcMDERlZmF1bHQgQ2l0eTEcMBoGA1UE
CgwTRGVmYXVsdCBDb21wYW55IEx0ZDAeFw0yNDAxMDkxMzA0MzFaFw0zNDAxMDYx
MzA0MzFaMEIxCzAJBgNVBAYTAlhYMRUwEwYDVQQHDAxEZWZhdWx0IENpdHkxHDAa
BgNVBAoME0RlZmF1bHQgQ29tcGFueSBMdGQwggEiMA0GCSqGSIb3DQEBAQUAA4IB
DwAwggEKAoIBAQC+Z9BqGKzPINWCSMdeTX52gLDoeTU3q4fH8QyHSO3hNo/eKtaE
rHOqnsn/ntcsjFwX9Wfwxt1B73uqXkqWWCsH2QKGsw36gPJmSc6ZuqP7oUTApx0U
OktdxOm96MouqN5OAXoPvzH5dFytJyW3TWpKJ3jP9ZWJrqmp4YcgnU+U6Vlen4iy
N0NciJtdVDDsWoWqh0zg0YOHJpd43c7aQ0PFoPp4QEj4j29I7X91UmRP67dA8kSw
2mPcZZFDkKY9fA0TuF1a3Dvx7yssvQoAC9F+jZYgBsTcFNGcc2roJVA8RwcdVZQ3
bTwA0nLql5EDLdXHSthJiXHPhp6niOTsJx4bAgMBAAGjUzBRMB0GA1UdDgQWBBQr
bklK4KlO6lgMM5MpVxqWcpWhxzAfBgNVHSMEGDAWgBQrbklK4KlO6lgMM5MpVxqW
cpWhxzAPBgNVHRMBAf8EBTADAQH/MA0GCSqGSIb3DQEBCwUAA4IBAQAEF3pv541h
XKwDMqHShbpvqEqnhN74c6zc6b8nnIohRK5rNkEkIEf2ikJ6Pdik2te4IHEoA4V9
HqtpKUtgNqge6GAw/p3kOB4C6eObZYZTaJ4ZiQ5UvO6R7w5MvFkjQH5hFO+fjhQv
8whWWO7HRlt/Hll/VF3JNVALtIv2SGi51WHFqwe+ERKl0kGKWH8PyY4X6XflHQfa
1FDev/NRnOVjRcXipsaZwRXcjRUiRX1KuOixlc8Reul8RdrL1Mt7lpl8+e/hqhoO
2O5thhnTuV/mID3zE+5J8w6UcCdeZo4VDNWdZqPzrI/ymSgARVwUu0MFeYfCbMmB
eEpiSixb6YRM
-----END CERTIFICATE-----
`

var _ = Describe("GetAssistedHTTPClient", func() {
	var (
		c client.Client
	)

	BeforeEach(func() {
		s := runtime.NewScheme()
		utilruntime.Must(corev1.AddToScheme(s))
		c = fakeclient.NewClientBuilder().WithScheme(s).Build()
	})

	It("succeeds when the CM name and namespace are empty", func() {
		config := ServiceConfig{}
		_, err := GetAssistedHTTPClient(config, c)
		Expect(err).To(BeNil())
	})

	It("fails when only one of the CM name or namespace is provided", func() {
		config := ServiceConfig{
			AssistedCABundleName: "test-cm-name",
		}
		_, err := GetAssistedHTTPClient(config, c)
		Expect(err).ToNot(BeNil())

		config = ServiceConfig{
			AssistedCABundleNamespace: "test-cm-namespace",
		}
		_, err = GetAssistedHTTPClient(config, c)
		Expect(err).ToNot(BeNil())
	})

	It("fails when the referenced CM key is not present", func() {
		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-cm-name",
				Namespace: "test-cm-namespace",
			},
			Data: map[string]string{
				"foo": "bar",
			},
		}
		Expect(c.Create(context.Background(), cm)).To(Succeed())

		config := ServiceConfig{
			AssistedCABundleName:      "test-cm-name",
			AssistedCABundleNamespace: "test-cm-namespace",
			AssistedCABundleKey:       "bundle.crt",
		}
		_, err := GetAssistedHTTPClient(config, c)
		Expect(err).ToNot(BeNil())
	})

	It("creates a client using the referenced certs", func() {
		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-cm-name",
				Namespace: "test-cm-namespace",
			},
			Data: map[string]string{
				"bundle.crt": testCert,
			},
		}
		Expect(c.Create(context.Background(), cm)).To(Succeed())

		config := ServiceConfig{
			AssistedCABundleName:      "test-cm-name",
			AssistedCABundleNamespace: "test-cm-namespace",
			AssistedCABundleKey:       "bundle.crt",
		}
		_, err := GetAssistedHTTPClient(config, c)
		Expect(err).To(BeNil())
	})
})
