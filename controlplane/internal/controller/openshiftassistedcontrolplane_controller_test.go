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

	"sigs.k8s.io/cluster-api/util/conditions"

	"github.com/golang/mock/gomock"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	hivev1 "github.com/openshift/hive/apis/hive/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	controlplanev1alpha2 "github.com/openshift-assisted/cluster-api-agent/controlplane/api/v1alpha2"
	testutils "github.com/openshift-assisted/cluster-api-agent/test/utils"
	configv1 "github.com/openshift/api/config/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
)

type mockOpenShiftVersioner struct {
}

func (m *mockOpenShiftVersioner) GetK8sVersionFromReleaseImage(ctx context.Context, releaseImage string, oacp *controlplanev1alpha2.OpenshiftAssistedControlPlane) (*string, error) {
	version := "1.30.0"
	return &version, nil
}

var _ = Describe("OpenshiftAssistedControlPlane Controller", func() {
	Context("When reconciling a resource", func() {
		const (
			openshiftAssistedControlPlaneName = "test-resource"
			clusterName                       = "test-cluster"
			namespace                         = "test"
		)
		var (
			ctx                  = context.Background()
			typeNamespacedName   types.NamespacedName
			cluster              *clusterv1.Cluster
			controllerReconciler *OpenshiftAssistedControlPlaneReconciler
			mockCtrl             *gomock.Controller
			k8sClient            client.Client
		)

		BeforeEach(func() {
			k8sClient = fakeclient.NewClientBuilder().
				WithScheme(testScheme).
				WithStatusSubresource(&controlplanev1alpha2.OpenshiftAssistedControlPlane{}).Build()

			mockCtrl = gomock.NewController(GinkgoT())
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}
			Expect(k8sClient.Create(ctx, ns)).To(Succeed())
			typeNamespacedName = types.NamespacedName{
				Name:      openshiftAssistedControlPlaneName,
				Namespace: namespace,
			}
			controllerReconciler = &OpenshiftAssistedControlPlaneReconciler{
				Client:           k8sClient,
				Scheme:           k8sClient.Scheme(),
				OpenShiftVersion: &mockOpenShiftVersioner{},
			}

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

		When("when a cluster owns this OpenshiftAssistedControlPlane", func() {
			It("should successfully create a cluster deployment", func() {
				By("setting the cluster as the owner ref on the OACP")

				openshiftAssistedControlPlane := getOpenshiftAssistedControlPlane()
				openshiftAssistedControlPlane.SetOwnerReferences(
					[]metav1.OwnerReference{
						*metav1.NewControllerRef(cluster, clusterv1.GroupVersion.WithKind(clusterv1.ClusterKind)),
					},
				)
				Expect(k8sClient.Create(ctx, openshiftAssistedControlPlane)).To(Succeed())

				By("checking if the OpenshiftAssistedControlPlane created the cluster deployment after reconcile")
				_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
					NamespacedName: typeNamespacedName,
				})
				expectedVersion := "1.30.0"
				Expect(err).NotTo(HaveOccurred())
				err = k8sClient.Get(ctx, typeNamespacedName, openshiftAssistedControlPlane)
				Expect(err).NotTo(HaveOccurred())
				Expect(openshiftAssistedControlPlane.Status.ClusterDeploymentRef).NotTo(BeNil())
				Expect(openshiftAssistedControlPlane.Status.ClusterDeploymentRef.Name).Should(Equal(openshiftAssistedControlPlane.Name))
				Expect(openshiftAssistedControlPlane.Status.Version).To(Equal(&expectedVersion))
				By("checking that the cluster deployment was created")
				cd := &hivev1.ClusterDeployment{}
				err = k8sClient.Get(ctx, typeNamespacedName, cd)
				Expect(err).NotTo(HaveOccurred())
				Expect(cd).NotTo(BeNil())

				// assert ClusterDeployment properties
				Expect(cd.Spec.ClusterName).To(Equal(clusterName))
				Expect(cd.Spec.BaseDomain).To(Equal(openshiftAssistedControlPlane.Spec.Config.BaseDomain))
				Expect(cd.Spec.PullSecretRef).To(Equal(openshiftAssistedControlPlane.Spec.Config.PullSecretRef))
			})
		})

		When("a cluster owns this OpenshiftAssistedControlPlane", func() {
			It("should successfully create a cluster deployment and match OpenshiftAssistedControlPlane properties", func() {
				By("setting the cluster as the owner ref on the OpenshiftAssistedControlPlane")

				openshiftAssistedControlPlane := getOpenshiftAssistedControlPlane()
				openshiftAssistedControlPlane.Spec.Config.ClusterName = "my-cluster"
				openshiftAssistedControlPlane.Spec.Config.PullSecretRef = &corev1.LocalObjectReference{
					Name: "my-pullsecret",
				}
				openshiftAssistedControlPlane.Spec.Config.BaseDomain = "example.com"
				openshiftAssistedControlPlane.SetOwnerReferences(
					[]metav1.OwnerReference{
						*metav1.NewControllerRef(cluster, clusterv1.GroupVersion.WithKind(clusterv1.ClusterKind)),
					},
				)
				// Simulate AgentClusterInstall marking KubeconfigAvailableCondition and ControlPlaneReadyCondition
				conditions.MarkTrue(openshiftAssistedControlPlane, controlplanev1alpha2.KubeconfigAvailableCondition)
				conditions.MarkTrue(openshiftAssistedControlPlane, controlplanev1alpha2.ControlPlaneReadyCondition)

				Expect(k8sClient.Create(ctx, openshiftAssistedControlPlane)).To(Succeed())

				By("checking if the OpenshiftAssistedControlPlane created the cluster deployment after reconcile")
				_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
					NamespacedName: typeNamespacedName,
				})
				Expect(err).NotTo(HaveOccurred())
				err = k8sClient.Get(ctx, typeNamespacedName, openshiftAssistedControlPlane)
				Expect(err).NotTo(HaveOccurred())
				Expect(openshiftAssistedControlPlane.Status.ClusterDeploymentRef).NotTo(BeNil())
				Expect(openshiftAssistedControlPlane.Status.ClusterDeploymentRef.Name).Should(Equal(openshiftAssistedControlPlane.Name))
				By("checking conditions ready")
				expectedReadyConditions := []clusterv1.ConditionType{
					//controlplanev1alpha2.ControlPlaneReadyCondition,
					controlplanev1alpha2.MachinesCreatedCondition,
					controlplanev1alpha2.KubeconfigAvailableCondition,
					controlplanev1alpha2.KubernetesVersionAvailableCondition,
				}
				checkReadyConditions(expectedReadyConditions, openshiftAssistedControlPlane)

				By("checking that the cluster deployment was created")
				cd := &hivev1.ClusterDeployment{}
				err = k8sClient.Get(ctx, typeNamespacedName, cd)
				Expect(err).NotTo(HaveOccurred())
				Expect(cd).NotTo(BeNil())

				// assert ClusterDeployment properties
				Expect(cd.Spec.ClusterName).To(Equal(openshiftAssistedControlPlane.Spec.Config.ClusterName))
				Expect(cd.Spec.BaseDomain).To(Equal(openshiftAssistedControlPlane.Spec.Config.BaseDomain))
				Expect(cd.Spec.PullSecretRef).To(Equal(openshiftAssistedControlPlane.Spec.Config.PullSecretRef))
			})
		})

		When("a pull secret isn't set on the OpenshiftAssistedControlPlane", func() {
			It("should successfully create the ClusterDeployment with the default fake pull secret", func() {
				By("setting the cluster as the owner ref on the OpenshiftAssistedControlPlane")
				openshiftAssistedControlPlane := getOpenshiftAssistedControlPlane()
				openshiftAssistedControlPlane.SetOwnerReferences(
					[]metav1.OwnerReference{
						*metav1.NewControllerRef(cluster, clusterv1.GroupVersion.WithKind(clusterv1.ClusterKind)),
					},
				)
				Expect(k8sClient.Create(ctx, openshiftAssistedControlPlane)).To(Succeed())

				By("checking if the OpenshiftAssistedControlPlane created the cluster deployment after reconcile")
				_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
					NamespacedName: typeNamespacedName,
				})
				Expect(err).NotTo(HaveOccurred())
				err = k8sClient.Get(ctx, typeNamespacedName, openshiftAssistedControlPlane)
				Expect(err).NotTo(HaveOccurred())
				Expect(openshiftAssistedControlPlane.Status.ClusterDeploymentRef).NotTo(BeNil())
				Expect(openshiftAssistedControlPlane.Status.ClusterDeploymentRef.Name).Should(Equal(openshiftAssistedControlPlane.Name))

				By("checking that the fake pull secret was created")
				pullSecret := &corev1.Secret{}
				err = k8sClient.Get(ctx, types.NamespacedName{Name: placeholderPullSecretName, Namespace: namespace}, pullSecret)
				Expect(err).NotTo(HaveOccurred())
				Expect(pullSecret).NotTo(BeNil())
				Expect(pullSecret.Data).NotTo(BeNil())
				Expect(pullSecret.Data).To(HaveKey(".dockerconfigjson"))

				By("checking that the cluster deployment was created and references the fake pull secret")
				cd := &hivev1.ClusterDeployment{}
				err = k8sClient.Get(ctx, typeNamespacedName, cd)
				Expect(err).NotTo(HaveOccurred())
				Expect(cd).NotTo(BeNil())
				Expect(cd.Spec.PullSecretRef).NotTo(BeNil())
				Expect(cd.Spec.PullSecretRef.Name).To(Equal(placeholderPullSecretName))

			})
		})
		It("should add a finalizer to the OpenshiftAssistedControlPlane if it's not being deleted", func() {
			By("setting the owner ref on the OpenshiftAssistedControlPlane")

			openshiftAssistedControlPlane := getOpenshiftAssistedControlPlane()
			openshiftAssistedControlPlane.SetOwnerReferences(
				[]metav1.OwnerReference{
					*metav1.NewControllerRef(cluster, clusterv1.GroupVersion.WithKind(clusterv1.ClusterKind)),
				},
			)
			Expect(k8sClient.Create(ctx, openshiftAssistedControlPlane)).To(Succeed())

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("checking if the OpenshiftAssistedControlPlane has the finalizer")
			err = k8sClient.Get(ctx, typeNamespacedName, openshiftAssistedControlPlane)
			Expect(err).NotTo(HaveOccurred())
			Expect(openshiftAssistedControlPlane.Finalizers).NotTo(BeEmpty())
			Expect(openshiftAssistedControlPlane.Finalizers).To(ContainElement(acpFinalizer))
		})
		When("an invalid version is set on the OpenshiftAssistedControlPlane", func() {
			It("should return error", func() {
				By("setting the cluster as the owner ref on the OpenshiftAssistedControlPlane")
				openshiftAssistedControlPlane := getOpenshiftAssistedControlPlane()
				openshiftAssistedControlPlane.Spec.DistributionVersion = "4.12.0"
				openshiftAssistedControlPlane.SetOwnerReferences(
					[]metav1.OwnerReference{
						*metav1.NewControllerRef(cluster, clusterv1.GroupVersion.WithKind(clusterv1.ClusterKind)),
					},
				)
				Expect(k8sClient.Create(ctx, openshiftAssistedControlPlane)).To(Succeed())
				By("checking if the condition of invalid version")
				_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
					NamespacedName: typeNamespacedName,
				})
				Expect(err).NotTo(HaveOccurred())

				Expect(k8sClient.Get(ctx, typeNamespacedName, openshiftAssistedControlPlane)).To(Succeed())

				condition := conditions.Get(openshiftAssistedControlPlane,
					controlplanev1alpha2.MachinesCreatedCondition,
				)
				Expect(condition).NotTo(BeNil())
				Expect(condition.Message).To(Equal("version 4.12.0 is not supported, the minimum supported version is 4.14.0"))

			})
		})
	})
})

var _ = Describe("Upgrade", func() {
	Context("distributionVersion returned", func() {
		const (
			openshiftAssistedControlPlaneName = "test-resource"
			clusterName                       = "test-cluster"
			namespace                         = "test"
		)
		var (
			ctx                         = context.Background()
			cluster                     *clusterv1.Cluster
			controllerReconciler        *OpenshiftAssistedControlPlaneReconciler
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
			controllerReconciler = &OpenshiftAssistedControlPlaneReconciler{
				Client:                         k8sClient,
				Scheme:                         k8sClient.Scheme(),
				OpenShiftVersion:               &mockOpenShiftVersioner{},
				WorkloadClusterClientGenerator: mockWorkloadClientGenerator,
			}

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
		When("the kubeconfig secret is not yet available for the workload cluster", func() {
			It("should return an error and the distribution version should be empty", func() {
				By("creating the openshiftassistedcontrolplane with condition kubeconfig available set to false")
				openshiftAssistedControlPlane := getOpenshiftAssistedControlPlane()
				conditions.MarkFalse(
					openshiftAssistedControlPlane,
					controlplanev1alpha2.KubeconfigAvailableCondition,
					controlplanev1alpha2.KubeconfigUnavailableFailedReason,
					clusterv1.ConditionSeverityInfo,
					"error retrieving Kubeconfig %v", fmt.Errorf("secret does not exist"),
				)

				By("confirming an error is returned when calling getWorkloadClusterVersion")
				_, err := controllerReconciler.getWorkloadClusterVersion(ctx, openshiftAssistedControlPlane)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(Equal("kubeconfig for workload cluster is not available yet"))
			})
		})

		When("the kubeconfig secret is available", func() {
			It("should return the openshiftassistedcontrolplane's distributionversion", func() {
				By("creating the openshiftassistedcontrolplane with condition kubeconfig available set to true")
				openshiftAssistedControlPlane := getOpenshiftAssistedControlPlane()
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
				distributionVersion, err := controllerReconciler.getWorkloadClusterVersion(ctx, openshiftAssistedControlPlane)
				Expect(err).NotTo(HaveOccurred())
				Expect(distributionVersion).To(Equal(openshiftAssistedControlPlane.Spec.DistributionVersion))
			})
		})
	})
	Context("isUpgradeRequested", func() {
		When("openshiftassistedcontrolplane doesn't have the distributionVersion status set", func() {
			It("returns false", func() {
				openshiftAssistedControlPlane := getOpenshiftAssistedControlPlane()
				Expect(isUpgradeRequested(context.Background(), openshiftAssistedControlPlane)).To(BeFalse())
			})
		})
		When("openshiftassistedcontrolplane's requested upgrade is less than the current workload cluster's version", func() {
			It("returns false", func() {
				openshiftAssistedControlPlane := getOpenshiftAssistedControlPlane()
				openshiftAssistedControlPlane.Status.DistributionVersion = "4.18.0"
				Expect(isUpgradeRequested(context.Background(), openshiftAssistedControlPlane)).To(BeFalse())
			})
		})
		When("openshiftassistedcontrolplane's requested upgrade is greater than the current workload cluster's version", func() {
			It("returns true", func() {
				openshiftAssistedControlPlane := getOpenshiftAssistedControlPlane()
				openshiftAssistedControlPlane.Status.DistributionVersion = "4.11.0"
				Expect(isUpgradeRequested(context.Background(), openshiftAssistedControlPlane)).To(BeTrue())
			})
		})
	})
})

func checkReadyConditions(expectedReadyConditions []clusterv1.ConditionType, openshiftAssistedControlPlane *controlplanev1alpha2.OpenshiftAssistedControlPlane) {
	for _, conditionType := range expectedReadyConditions {
		By(fmt.Sprintf("checking condition %s ready", conditionType))
		condition := conditions.Get(openshiftAssistedControlPlane,
			conditionType,
		)
		Expect(condition).NotTo(BeNil())
		Expect(condition.Status).To(Equal(corev1.ConditionTrue))
	}
}

func getOpenshiftAssistedControlPlane() *controlplanev1alpha2.OpenshiftAssistedControlPlane {
	return &controlplanev1alpha2.OpenshiftAssistedControlPlane{
		ObjectMeta: metav1.ObjectMeta{
			Name:      openshiftAssistedControlPlaneName,
			Namespace: namespace,
		},
		Spec: controlplanev1alpha2.OpenshiftAssistedControlPlaneSpec{
			DistributionVersion: "4.16.0",
		},
	}
}

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
