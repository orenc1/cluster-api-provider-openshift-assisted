package imageregistry

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/golang/mock/gomock"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	configv1 "github.com/openshift/api/config/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

var k8sClient client.Client

func TestControllers(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "ImageRegistry Suite")
}

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	var err error
	err = configv1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())
	err = corev1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	//+kubebuilder:scaffold:scheme
	k8sClient = fakeclient.NewClientBuilder().WithScheme(scheme.Scheme).Build()
	Expect(k8sClient).NotTo(BeNil())
})

var _ = Describe("ImageRegistry Test", func() {
	Context("CreateConfig", func() {
		var (
			ctx           = context.Background()
			mockCtrl      *gomock.Controller
			registryRefCM *corev1.ConfigMap
		)

		const (
			providedCMName = "user-provided-config-map"
			namespace      = "test-namespace"
			certificate    = "    -----BEGIN CERTIFICATE-----\n    certificate contents\n    -----END CERTIFICATE------"
			sourceRegistry = "quay.io"
			mirrorRegistry = "example-user-registry.com"
		)

		BeforeEach(func() {
			mockCtrl = gomock.NewController(GinkgoT())
			registryRefCM = generateUserProvidedRegistryCM(providedCMName, namespace, getRegistryToml(sourceRegistry, mirrorRegistry, false, false), certificate)
		})

		AfterEach(func() {
			imageRegistryConfigMap := &corev1.ConfigMap{}
			k8sClient.Get(ctx, types.NamespacedName{Name: imageConfigMapName, Namespace: namespace}, imageRegistryConfigMap)
			k8sClient.Delete(ctx, imageRegistryConfigMap)
			mockCtrl.Finish()
		})

		It("creates the testing namespace", func() {
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}
			Expect(k8sClient.Create(ctx, ns)).To(Succeed())
		})

		It("successfully creates the image registry configmap for the spoke cluster", func() {
			By("Creating the user-provided configmap")
			Expect(k8sClient.Create(ctx, registryRefCM)).To(Succeed())

			By("Calling the CreateConfig function")
			configMapName, err := CreateConfig(ctx, k8sClient, &corev1.LocalObjectReference{Name: registryRefCM.Name}, namespace)
			Expect(err).NotTo(HaveOccurred())
			Expect(configMapName).To(Equal(imageConfigMapName))

			By("Checking that the ConfigMap was created")
			imageRegistryConfigMap := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: configMapName, Namespace: namespace}, imageRegistryConfigMap)).To(Succeed())

			expectedImageDigestMirrorSet := getImageDigestMirrorSetString(sourceRegistry, []string{mirrorRegistry})
			expectedClusterImage := getImageConfigString(registryCertConfigMapName, []string{})
			expectedCertificateCM := getCMString(registryCertConfigMapName, registryCertConfigMapNamespace, map[string]string{registryCertKey: certificate})

			By("Checking the ConfigMap contains the correct data")
			Expect(imageRegistryConfigMap.Data).NotTo(BeNil())
			Expect(imageRegistryConfigMap.Data).NotTo(HaveKey(imageTagMirrorSetKey))
			Expect(imageRegistryConfigMap.Data).To(HaveKey(imageDigestMirrorSetKey))
			Expect(imageRegistryConfigMap.Data[imageDigestMirrorSetKey]).To(Equal(expectedImageDigestMirrorSet))
			Expect(imageRegistryConfigMap.Data).To(HaveKey(registryCertConfigMapKey))
			Expect(imageRegistryConfigMap.Data[registryCertConfigMapKey]).To(Equal(expectedCertificateCM))
			Expect(imageRegistryConfigMap.Data).To(HaveKey(imageConfigKey))
			Expect(imageRegistryConfigMap.Data[imageConfigKey]).To(Equal(expectedClusterImage))
		})

		It("successfully creates the image registry configmap with an insecure registry", func() {
			By("Updating the user created ConfigMap to use an insecure registry")
			updatedRegistryRefCM := generateUserProvidedRegistryCM(providedCMName, namespace, getRegistryToml(sourceRegistry, mirrorRegistry, true, false), certificate)
			Expect(k8sClient.Update(ctx, updatedRegistryRefCM)).To(Succeed())

			By("Calling the CreateConfig function")
			configMapName, err := CreateConfig(ctx, k8sClient, &corev1.LocalObjectReference{Name: registryRefCM.Name}, namespace)
			Expect(err).NotTo(HaveOccurred())
			Expect(configMapName).To(Equal(imageConfigMapName))

			By("Checking that the ConfigMap was created")
			imageRegistryConfigMap := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: configMapName, Namespace: namespace}, imageRegistryConfigMap)).To(Succeed())

			insecureRegistries := []string{mirrorRegistry}
			expectedImageDigestMirrorSet := getImageDigestMirrorSetString(sourceRegistry, []string{mirrorRegistry})
			expectedClusterImage := getImageConfigString(registryCertConfigMapName, insecureRegistries)
			expectedCertificateCM := getCMString(registryCertConfigMapName, registryCertConfigMapNamespace, map[string]string{registryCertKey: certificate})

			By("Checking the ConfigMap contains the correct data")
			Expect(imageRegistryConfigMap.Data).NotTo(BeNil())
			Expect(imageRegistryConfigMap.Data).NotTo(HaveKey(imageTagMirrorSetKey))
			Expect(imageRegistryConfigMap.Data).To(HaveKey(imageDigestMirrorSetKey))
			Expect(imageRegistryConfigMap.Data[imageDigestMirrorSetKey]).To(Equal(expectedImageDigestMirrorSet))
			Expect(imageRegistryConfigMap.Data).To(HaveKey(registryCertConfigMapKey))
			Expect(imageRegistryConfigMap.Data[registryCertConfigMapKey]).To(Equal(expectedCertificateCM))
			Expect(imageRegistryConfigMap.Data).To(HaveKey(imageConfigKey))
			Expect(imageRegistryConfigMap.Data[imageConfigKey]).To(Equal(expectedClusterImage))
		})

		It("successfully creates the image registry configmap that pulls from tag", func() {
			By("Updating the user created ConfigMap to remove the mirror registry")
			updatedRegistryRefCM := generateUserProvidedRegistryCM(providedCMName, namespace, getRegistryToml(sourceRegistry, mirrorRegistry, false, true), certificate)
			Expect(k8sClient.Update(ctx, updatedRegistryRefCM)).To(Succeed())

			By("Calling the CreateConfig function")
			configMapName, err := CreateConfig(ctx, k8sClient, &corev1.LocalObjectReference{Name: registryRefCM.Name}, namespace)
			Expect(err).NotTo(HaveOccurred())
			Expect(configMapName).To(Equal(imageConfigMapName))

			By("Checking that the ConfigMap was created")
			imageRegistryConfigMap := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: configMapName, Namespace: namespace}, imageRegistryConfigMap)).To(Succeed())

			expectedImageTagMirrorSet := getImagTagMirrorSetString(sourceRegistry, []string{mirrorRegistry})
			expectedClusterImage := getImageConfigString(registryCertConfigMapName, []string{})
			expectedCertificateCM := getCMString(registryCertConfigMapName, registryCertConfigMapNamespace, map[string]string{registryCertKey: certificate})

			By("Checking the ConfigMap contains the correct data")
			Expect(imageRegistryConfigMap.Data).NotTo(BeNil())
			Expect(imageRegistryConfigMap.Data).NotTo(HaveKey(imageDigestMirrorSetKey))
			Expect(imageRegistryConfigMap.Data).To(HaveKey(imageTagMirrorSetKey))
			Expect(imageRegistryConfigMap.Data[imageTagMirrorSetKey]).To(Equal(expectedImageTagMirrorSet))
			Expect(imageRegistryConfigMap.Data).To(HaveKey(registryCertConfigMapKey))
			Expect(imageRegistryConfigMap.Data[registryCertConfigMapKey]).To(Equal(expectedCertificateCM))
			Expect(imageRegistryConfigMap.Data).To(HaveKey(imageConfigKey))
			Expect(imageRegistryConfigMap.Data[imageConfigKey]).To(Equal(expectedClusterImage))
		})

		It("fails to create the image registry configmap when the mirror registry is missing", func() {
			By("Updating the user created ConfigMap to remove the mirror registry")
			updatedRegistryRefCM := generateUserProvidedRegistryCM(providedCMName, namespace, getRegistryToml(sourceRegistry, "", false, false), certificate)
			Expect(k8sClient.Update(ctx, updatedRegistryRefCM)).To(Succeed())

			By("Calling the CreateConfig function")
			configMapName, err := CreateConfig(ctx, k8sClient, &corev1.LocalObjectReference{Name: registryRefCM.Name}, namespace)
			Expect(err).To(HaveOccurred())
			Expect(configMapName).To(BeEmpty())

			By("Ensuring the ConfigMap wasn't created")
			imageRegistryConfigMap := &corev1.ConfigMap{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: configMapName, Namespace: namespace}, imageRegistryConfigMap)
			Expect(err).NotTo(BeNil())
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
		})

		It("fails to create the image registry configmap when the registry toml is not well-formatted", func() {
			By("Updating the user created ConfigMap to use an incorrect registry toml")
			updatedRegistryRefCM := generateUserProvidedRegistryCM(providedCMName, namespace, fmt.Sprintf("location=%s", sourceRegistry), certificate)
			Expect(k8sClient.Update(ctx, updatedRegistryRefCM)).To(Succeed())

			By("Calling the CreateConfig function")
			configMapName, err := CreateConfig(ctx, k8sClient, &corev1.LocalObjectReference{Name: registryRefCM.Name}, namespace)
			Expect(err).To(HaveOccurred())
			Expect(configMapName).To(BeEmpty())

			By("Ensuring the ConfigMap wasn't created")
			imageRegistryConfigMap := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: configMapName, Namespace: namespace}, imageRegistryConfigMap)).NotTo(Succeed())
		})

		It("successfully creates the image registry configmap when there are no additional certificates", func() {
			By("Updating the user created ConfigMap to remove the certificate")
			newRegistryCM := registryRefCM.DeepCopy()
			delete(newRegistryCM.Data, registryCertKey)
			Expect(k8sClient.Update(ctx, newRegistryCM)).To(Succeed())

			By("Calling the CreateConfig function")
			configMapName, err := CreateConfig(ctx, k8sClient, &corev1.LocalObjectReference{Name: registryRefCM.Name}, namespace)
			Expect(err).NotTo(HaveOccurred())
			Expect(configMapName).To(Equal(imageConfigMapName))

			By("Checking that the ConfigMap was created")
			imageRegistryConfigMap := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: configMapName, Namespace: namespace}, imageRegistryConfigMap)).To(Succeed())

			expectedImageDigestMirrorSet := getImageDigestMirrorSetString(sourceRegistry, []string{mirrorRegistry})

			By("Checking the ConfigMap contains the correct data")
			Expect(imageRegistryConfigMap.Data).NotTo(BeNil())
			Expect(imageRegistryConfigMap.Data[imageDigestMirrorSetKey]).NotTo(BeNil())
			Expect(imageRegistryConfigMap.Data[imageDigestMirrorSetKey]).To(Equal(expectedImageDigestMirrorSet))
			Expect(imageRegistryConfigMap.Data[registryCertConfigMapKey]).To(BeEmpty())
			Expect(imageRegistryConfigMap.Data[imageConfigKey]).To(BeEmpty())
		})

		It("fails to create the image registry configmap when the user configmap doesn't exist", func() {
			By("Deleting the user created ConfigMap")
			Expect(k8sClient.Delete(ctx, registryRefCM)).To(Succeed())

			By("Calling the CreateConfig function")
			configMapName, err := CreateConfig(ctx, k8sClient, &corev1.LocalObjectReference{Name: registryRefCM.Name}, namespace)
			Expect(err).To(HaveOccurred())
			Expect(configMapName).To(BeEmpty())

			By("Ensuring the ConfigMap wasn't created")
			imageRegistryConfigMap := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: configMapName, Namespace: namespace}, imageRegistryConfigMap)).NotTo(Succeed())
		})

		It("fails to create the image registry configmap when the user configmap doesn't contain the registry config key", func() {
			By("Creating the user created ConfigMap")
			newRegistryCM := registryRefCM.DeepCopy()
			delete(newRegistryCM.Data, registryConfKey)
			Expect(k8sClient.Create(ctx, newRegistryCM)).To(Succeed())

			By("Calling the CreateConfig function")
			configMapName, err := CreateConfig(ctx, k8sClient, &corev1.LocalObjectReference{Name: registryRefCM.Name}, namespace)
			Expect(err).To(HaveOccurred())
			Expect(configMapName).To(BeEmpty())

			By("Ensuring the ConfigMap wasn't created")
			imageRegistryConfigMap := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: configMapName, Namespace: namespace}, imageRegistryConfigMap)).NotTo(Succeed())
		})
	})
})

func getRegistryToml(source, mirror string, insecure, pullByTag bool) string {
	registryToml := ""
	if source != "" {
		registryToml = fmt.Sprintf("[[registry]]\nlocation = \"%s\"", source)
	}
	if mirror != "" {
		registryToml = fmt.Sprintf("%s\n[[registry.mirror]]\nlocation = \"%s\"", registryToml, mirror)
	}
	if insecure {
		registryToml = fmt.Sprintf("%s\ninsecure = true", registryToml)
	}
	if pullByTag {
		registryToml = fmt.Sprintf("%s\npull-from-mirror = \"tag-only\"", registryToml)
	}
	return registryToml
}

func generateUserProvidedRegistryCM(name, namespace, registry, certificate string) *corev1.ConfigMap {
	data := map[string]string{
		registryConfKey: registry,
		registryCertKey: certificate,
	}
	return generateConfigMap(name, namespace, data)
}

func generateConfigMap(name, namespace string, data map[string]string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Data: data,
	}
}

func getImageDigestMirrorSetString(source string, mirrors []string) string {
	imageDigestMirror := configv1.ImageDigestMirrors{}
	if source != "" {
		imageDigestMirror.Source = source
	}
	for _, mirror := range mirrors {
		imageDigestMirror.Mirrors = append(imageDigestMirror.Mirrors, configv1.ImageMirror(mirror))
	}

	expectedImageDigestMirrorSet, _ := json.Marshal(configv1.ImageDigestMirrorSet{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ImageDigestMirrorSet",
			APIVersion: configv1.GroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{Name: imageRegistryName},
		Spec: configv1.ImageDigestMirrorSetSpec{
			ImageDigestMirrors: []configv1.ImageDigestMirrors{imageDigestMirror},
		},
	})
	return string(expectedImageDigestMirrorSet)
}

func getImagTagMirrorSetString(source string, mirrors []string) string {
	imageTagMirror := configv1.ImageTagMirrors{}
	if source != "" {
		imageTagMirror.Source = source
	}
	for _, mirror := range mirrors {
		imageTagMirror.Mirrors = append(imageTagMirror.Mirrors, configv1.ImageMirror(mirror))
	}

	expectedImageTagMirrorSet, _ := json.Marshal(configv1.ImageTagMirrorSet{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ImageTagMirrorSet",
			APIVersion: configv1.GroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{Name: imageRegistryName},
		Spec: configv1.ImageTagMirrorSetSpec{
			ImageTagMirrors: []configv1.ImageTagMirrors{imageTagMirror},
		},
	})
	return string(expectedImageTagMirrorSet)
}

func getImageConfigString(certCMName string, insecureRegistries []string) string {
	imageConfig := configv1.Image{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Image",
			APIVersion: configv1.GroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec: configv1.ImageSpec{
			AdditionalTrustedCA: configv1.ConfigMapNameReference{Name: certCMName},
		},
	}
	if len(insecureRegistries) > 0 {
		imageConfig.Spec.RegistrySources.InsecureRegistries = insecureRegistries
	}
	imageConfigJSON, _ := json.Marshal(imageConfig)
	return string(imageConfigJSON)
}

func getCMString(name, namespace string, data map[string]string) string {
	configMap, _ := json.Marshal(corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ConfigMap",
			APIVersion: corev1.SchemeGroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Data: data,
	})
	return string(configMap)
}
