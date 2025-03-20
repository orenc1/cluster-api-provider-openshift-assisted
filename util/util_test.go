package util_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/openshift-assisted/cluster-api-agent/util"
	v1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"sigs.k8s.io/controller-runtime/pkg/client"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"
)

var _ = Describe("Utils", func() {
	var (
		ctx       = context.TODO()
		k8sClient client.Client
	)
	BeforeEach(func() {
		k8sClient = fakeclient.NewClientBuilder().WithScheme(testScheme).
			Build()
	})

	It("should create a resource and then update it", func() {
		replicas := int32(1)
		deployment := v1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "test-deployment", Namespace: "test-namespace"},
			Spec: v1.DeploymentSpec{
				Replicas: &replicas,
			},
		}
		Expect(util.CreateOrUpdate(ctx, k8sClient, &deployment)).To(Succeed())

		// test that this object was created as specs
		retrievedDeployment := v1.Deployment{}
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(&deployment), &retrievedDeployment)).To(Succeed())
		Expect(retrievedDeployment.Spec.Replicas).To(Equal(&replicas))

		// update the object
		replicas = 2
		deployment.Spec.Replicas = &replicas
		deployment.Annotations = map[string]string{"test": "test"}
		Expect(util.CreateOrUpdate(ctx, k8sClient, &deployment)).To(Succeed())

		// test that this object was updated as specs
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(&deployment), &retrievedDeployment)).To(Succeed())
		Expect("test").Should(BeKeyOf(retrievedDeployment.Annotations))
		Expect("test").To(Equal(retrievedDeployment.Annotations["test"]))
		Expect(retrievedDeployment.Spec.Replicas).To(Equal(&replicas))
	})
})
