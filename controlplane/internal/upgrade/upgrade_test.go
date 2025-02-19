package upgrade

import (
	"context"
	"fmt"
	"testing"

	"github.com/golang/mock/gomock"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"sigs.k8s.io/cluster-api/util/conditions"
	"sigs.k8s.io/controller-runtime/pkg/client"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	controlplanev1alpha2 "github.com/openshift-assisted/cluster-api-agent/controlplane/api/v1alpha2"
	testutils "github.com/openshift-assisted/cluster-api-agent/test/utils"

	configv1 "github.com/openshift/api/config/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
)

func TestControllers(t *testing.T) {
	RegisterFailHandler(Fail)

	RunSpecs(t, "Upgrade")
}

var testScheme = runtime.NewScheme()

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	By("bootstrapping test environment")

	utilruntime.Must(controlplanev1alpha2.AddToScheme(testScheme))
	utilruntime.Must(corev1.AddToScheme(testScheme))
	utilruntime.Must(configv1.AddToScheme(testScheme))
	utilruntime.Must(clusterv1.AddToScheme(testScheme))
	//+kubebuilder:scaffold:scheme

})

var _ = Describe("Upgrade", func() {
	const (
		openshiftAssistedControlPlaneName = "test-resource"
		clusterName                       = "test-cluster"
		namespace                         = "test"
	)
	var (
		ctx     = context.Background()
		cluster *clusterv1.Cluster

		mockCtrl                    *gomock.Controller
		k8sClient                   client.Client
		mockWorkloadClientGenerator *mockWorkloadClient
	)
	BeforeEach(func() {
		k8sClient = fakeclient.NewClientBuilder().
			WithScheme(testScheme).
			WithStatusSubresource(&controlplanev1alpha2.OpenshiftAssistedControlPlane{}).Build()

		mockCtrl = gomock.NewController(GinkgoT())
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}
		Expect(k8sClient.Create(ctx, ns)).To(Succeed())

		mockWorkloadClientGenerator = NewMockWorkloadClient()

		By("creating a cluster")
		cluster = &clusterv1.Cluster{}
		err := k8sClient.Get(ctx, types.NamespacedName{Name: clusterName, Namespace: namespace}, cluster)
		if err != nil && errors.IsNotFound(err) {
			cluster = testutils.NewCluster(clusterName, namespace)
			err := k8sClient.Create(ctx, cluster)
			Expect(err).NotTo(HaveOccurred())
		}
	})

	AfterEach(func() {
		mockCtrl.Finish()
		k8sClient = nil
	})
	Context("GetWorkloadClusterVersion", func() {
		When("the kubeconfig secret is not yet available for the workload cluster", func() {
			It("should return an error and the distribution version should be empty", func() {
				By("creating the openshiftassistedcontrolplane with condition kubeconfig available set to false")
				openshiftAssistedControlPlane := testutils.NewOpenshiftAssistedControlPlane(namespace, openshiftAssistedControlPlaneName)
				conditions.MarkFalse(
					openshiftAssistedControlPlane,
					controlplanev1alpha2.KubeconfigAvailableCondition,
					controlplanev1alpha2.KubeconfigUnavailableFailedReason,
					clusterv1.ConditionSeverityInfo,
					"error retrieving Kubeconfig %v", fmt.Errorf("secret does not exist"),
				)

				By("confirming an error is returned when calling getWorkloadClusterVersion")
				_, err := GetWorkloadClusterVersion(ctx, k8sClient,
					mockWorkloadClientGenerator, openshiftAssistedControlPlane)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(Equal("kubeconfig for workload cluster is not available yet"))
			})
		})

		When("the kubeconfig secret is available", func() {
			It("should return the openshiftassistedcontrolplane's distributionversion", func() {
				By("creating the openshiftassistedcontrolplane with condition kubeconfig available set to true")
				openshiftAssistedControlPlane := testutils.NewOpenshiftAssistedControlPlane(namespace, openshiftAssistedControlPlaneName)
				openshiftAssistedControlPlane.Labels = map[string]string{
					clusterv1.ClusterNameLabel: cluster.Name,
				}
				conditions.MarkTrue(openshiftAssistedControlPlane, controlplanev1alpha2.KubeconfigAvailableCondition)
				By("creating the kubeconfig secret")
				kubeconfigSecret := &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      fmt.Sprintf("%s-kubeconfig", cluster.Name),
						Namespace: openshiftAssistedControlPlane.Namespace,
					},
					Data: map[string][]byte{
						"value": []byte("kubeconfig"),
					},
				}
				Expect(k8sClient.Create(ctx, kubeconfigSecret)).To(Succeed())
				By("creating a cluster version using the workload cluster's client")
				mockWorkloadClientGenerator.MockCreateClusterVersion(openshiftAssistedControlPlane.Spec.DistributionVersion)
				By("checking the cluster version returned")
				distributionVersion, err := GetWorkloadClusterVersion(ctx, k8sClient,
					mockWorkloadClientGenerator, openshiftAssistedControlPlane)
				Expect(err).NotTo(HaveOccurred())
				Expect(distributionVersion).To(Equal(openshiftAssistedControlPlane.Spec.DistributionVersion))
			})
		})
	})
	Context("IsUpgradeRequested", func() {
		When("openshiftassistedcontrolplane doesn't have the distributionVersion status set", func() {
			It("returns false", func() {
				openshiftAssistedControlPlane := testutils.NewOpenshiftAssistedControlPlane(namespace, openshiftAssistedControlPlaneName)
				Expect(IsUpgradeRequested(context.Background(), openshiftAssistedControlPlane)).To(BeFalse())
			})
		})
		When("openshiftassistedcontrolplane's requested upgrade is less than the current workload cluster's version", func() {
			It("returns false", func() {
				openshiftAssistedControlPlane := testutils.NewOpenshiftAssistedControlPlane(namespace, openshiftAssistedControlPlaneName)
				openshiftAssistedControlPlane.Status.DistributionVersion = "4.18.0"
				Expect(IsUpgradeRequested(context.Background(), openshiftAssistedControlPlane)).To(BeFalse())
			})
		})
		When("openshiftassistedcontrolplane's requested upgrade is greater than the current workload cluster's version", func() {
			It("returns true", func() {
				openshiftAssistedControlPlane := testutils.NewOpenshiftAssistedControlPlane(namespace, openshiftAssistedControlPlaneName)
				openshiftAssistedControlPlane.Status.DistributionVersion = "4.11.0"
				Expect(IsUpgradeRequested(context.Background(), openshiftAssistedControlPlane)).To(BeTrue())
			})
		})
	})
})

type mockWorkloadClient struct {
	mockClient client.Client
}

func NewMockWorkloadClient() *mockWorkloadClient {
	workloadK8sClient := fakeclient.NewClientBuilder().
		WithScheme(testScheme).Build()
	return &mockWorkloadClient{
		mockClient: workloadK8sClient,
	}
}

func (m *mockWorkloadClient) GetWorkloadClusterClient(kubeconfig []byte) (client.Client, error) {
	return m.mockClient, nil
}

func (m *mockWorkloadClient) MockCreateClusterVersion(version string) {
	clusterVersion := &configv1.ClusterVersion{
		ObjectMeta: metav1.ObjectMeta{
			Name: "version",
		},
		Status: configv1.ClusterVersionStatus{
			Desired: configv1.Release{
				Version: version,
			},
		},
	}
	Expect(m.mockClient.Create(context.Background(), clusterVersion)).To(Succeed())
}
