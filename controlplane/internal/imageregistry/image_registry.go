package imageregistry

import (
	"context"
	"encoding/json"
	"fmt"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/pelletier/go-toml"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	registryConfKey                = "registries.conf"
	registryCertKey                = "ca-bundle.crt"
	imageRegistryName              = "additional-registry"
	registryCertConfigMapName      = "additional-registry-certificate"
	registryCertConfigMapNamespace = "openshift-config"
	imageConfigMapName             = "additional-registry-config"
	imageDigestMirrorSetKey        = "image-digest-mirror-set.json"
	imageTagMirrorSetKey           = "image-tag-mirror-set.json"
	registryCertConfigMapKey       = "additional-registry-certificate.json"
	imageConfigKey                 = "image-config.json"
)

// CreateConfig creates a ConfigMap containing the manifests to be created on the spoke cluster.
// The manifests that are in the ConfigMap include an ImageDigestMirrorSet CR and/or an ImageTagMirrorSet CR with the mirror registry information,
// a ConfigMap containing the additional trusted certificates for the mirror registry, and an image.config.openshift.io
// CR that references the ConfigMap with the additional trusted certificate.
// Successful creation of the ConfigMap will return the name of the created ConfigMap to be added as a manifest reference
func CreateConfig(ctx context.Context, client client.Client, registryRef *corev1.LocalObjectReference, namespace string) (string, error) {
	imageRegistryConfigMap := &corev1.ConfigMap{}
	if err := client.Get(ctx, types.NamespacedName{Name: registryRef.Name, Namespace: namespace}, imageRegistryConfigMap); err != nil {
		return "", err
	}

	additionalRegistryConfigMap, err := createImageRegistryConfig(imageRegistryConfigMap, namespace)
	if err != nil {
		return "", err
	}

	if _, err := ctrl.CreateOrUpdate(ctx, client, additionalRegistryConfigMap, func() error { return nil }); err != nil && !apierrors.IsAlreadyExists(err) {
		return "", err
	}
	return additionalRegistryConfigMap.Name, nil
}

func createImageRegistryConfig(imageRegistry *corev1.ConfigMap, namespace string) (*corev1.ConfigMap, error) {
	data := make(map[string]string, 0)

	registryConf, ok := imageRegistry.Data[registryConfKey]
	if !ok {
		return nil, fmt.Errorf("failed to find registry key [%s] in configmap %s/%s for image registry configuration", registryConfKey, imageRegistry.Name, namespace)
	}

	imageDigestMirrors, imageTagMirrors, insecureRegistries, err := getImageRegistries(registryConf)
	if err != nil {
		return nil, err
	}
	if imageDigestMirrors != "" {
		data[imageDigestMirrorSetKey] = imageDigestMirrors
	}
	if imageTagMirrors != "" {
		data[imageTagMirrorSetKey] = imageTagMirrors
	}

	registryCert, registryCertExists := imageRegistry.Data[registryCertKey]
	if registryCertExists {
		registryCertConfigMap, err := generateRegistyCertificateConfigMap(registryCert)
		if err != nil {
			return nil, err
		}
		data[registryCertConfigMapKey] = string(registryCertConfigMap)
	}

	if registryCertExists || len(insecureRegistries) > 0 {
		imageConfigJSON, err := generateOpenshiftImageConfig(insecureRegistries, registryCertExists)
		if err != nil {
			return nil, err
		}
		data[imageConfigKey] = string(imageConfigJSON)
	}

	imageConfig := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      imageConfigMapName,
			Namespace: namespace,
		},
		Data: data,
	}
	return imageConfig, nil
}

// getImageRegistries reads a toml tree string with the structure:
//
// [[registry]]
//
//	 location = "source-registry"
//
//	   [[registry.mirror]]
//		  location = "mirror-registry"
//		  insecure = true # indicates to use an insecure connection to this mirror registry
//		  pull-from-mirror = "tag-only" # indicates to add this to an ImageTagMirrorSet
//
// It will convert it to an ImageDigestMirrorSet CR and/or an ImageTagMirrorSet.
// It will return these as marshalled JSON strings, and it will return a string list of insecure
// mirror registries (if they exist)
func getImageRegistries(registry string) (string, string, []string, error) {
	// Parse toml and add mirror registry to image digest mirror set
	tomlTree, err := toml.Load(registry)
	if err != nil {
		return "", "", nil, err
	}
	registriesTree, ok := tomlTree.Get("registry").([]*toml.Tree)
	if !ok {
		return "", "", nil, fmt.Errorf("failed to find registry in toml tree")
	}

	idmsMirrors := make([]configv1.ImageDigestMirrors, 0)
	itmsMirrors := make([]configv1.ImageTagMirrors, 0)
	insecureRegistries := make([]string, 0)

	// Add each registry and its mirrors as either an image digest mirror or an image tag mirror
	for _, registry := range registriesTree {
		source, sourceExists := registry.Get("location").(string)
		mirrorTrees, mirrorExists := registry.Get("mirror").([]*toml.Tree)
		if !sourceExists || !mirrorExists {
			continue
		}
		parseMirrorRegistries(&idmsMirrors, &itmsMirrors, mirrorTrees, &insecureRegistries, source)
	}

	if len(idmsMirrors) < 1 && len(itmsMirrors) < 1 {
		return "", "", nil, fmt.Errorf("failed to find any image mirrors in registry.conf")
	}

	idmsMirrorsString, err := generateImageDigestMirrorSet(idmsMirrors)
	if err != nil {
		return "", "", nil, err
	}

	itmsMirrorsString, err := generateImageTagMirrorSet(itmsMirrors)
	if err != nil {
		return "", "", nil, err
	}
	return idmsMirrorsString, itmsMirrorsString, insecureRegistries, nil
}

// parseMirrorRegistries takes a mirror registry toml tree and parses it into
// a list of image mirrors and a string list of insecure mirror registries.
func parseMirrorRegistries(idmsMirrors *[]configv1.ImageDigestMirrors, itmsMirrors *[]configv1.ImageTagMirrors, mirrorTrees []*toml.Tree, insecureRegistries *[]string, source string) {
	itmsMirror := configv1.ImageTagMirrors{}
	idmsMirror := configv1.ImageDigestMirrors{}
	for _, mirrorTree := range mirrorTrees {
		if mirror, ok := mirrorTree.Get("location").(string); ok {
			if insecure, ok := mirrorTree.Get("insecure").(bool); ok && insecure {
				*insecureRegistries = append(*insecureRegistries, mirror)
			}
			if pullFrom, ok := mirrorTree.Get("pull-from-mirror").(string); ok {
				switch pullFrom {
				case "tag-only":
					itmsMirror.Source = source
					itmsMirror.Mirrors = append(itmsMirror.Mirrors, configv1.ImageMirror(mirror))
				case "digest-only":
					idmsMirror.Source = source
					idmsMirror.Mirrors = append(idmsMirror.Mirrors, configv1.ImageMirror(mirror))
				}
			} else {
				// Default is pulling by digest
				idmsMirror.Source = source
				idmsMirror.Mirrors = append(idmsMirror.Mirrors, configv1.ImageMirror(mirror))
			}
		}
	}
	if len(itmsMirror.Mirrors) > 0 {
		*itmsMirrors = append(*itmsMirrors, itmsMirror)
	}
	if len(idmsMirror.Mirrors) > 0 {
		*idmsMirrors = append(*idmsMirrors, idmsMirror)
	}
}

func generateImageDigestMirrorSet(mirrors []configv1.ImageDigestMirrors) (string, error) {
	if len(mirrors) < 1 {
		return "", nil
	}
	idms, err := json.Marshal(configv1.ImageDigestMirrorSet{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ImageDigestMirrorSet",
			APIVersion: configv1.GroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: imageRegistryName,
		},
		Spec: configv1.ImageDigestMirrorSetSpec{
			ImageDigestMirrors: mirrors,
		},
	})
	return string(idms), err
}

func generateImageTagMirrorSet(mirrors []configv1.ImageTagMirrors) (string, error) {
	if len(mirrors) < 1 {
		return "", nil
	}
	itms, err := json.Marshal(configv1.ImageTagMirrorSet{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ImageTagMirrorSet",
			APIVersion: configv1.GroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: imageRegistryName,
		},
		Spec: configv1.ImageTagMirrorSetSpec{
			ImageTagMirrors: mirrors,
		},
	})
	return string(itms), err
}

// generateRegistyCertificateConfigMap creates a ConfigMap CR containing the registry certificate
// to be created on the spoke cluster and marshals the ConfigMap to JSON
func generateRegistyCertificateConfigMap(certificate string) ([]byte, error) {
	// Add certificate as a ConfigMap manifest to be created in the spoke cluster's openshift-config namespace
	certificateCM := &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ConfigMap",
			APIVersion: corev1.SchemeGroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      registryCertConfigMapName,
			Namespace: registryCertConfigMapNamespace,
		},
		Data: map[string]string{
			registryCertKey: certificate,
		},
	}
	return json.Marshal(certificateCM)
}

// generateOpenshiftImageConfig creates an image.config.openshift.io CR that references the additional
// regitry certificate ConfigMap CR if it exists and sets any insecure registries. It then marshals
// it into JSON.
func generateOpenshiftImageConfig(insecureRegistries []string, addRegistryCert bool) ([]byte, error) {
	clusterImageConfig := &configv1.Image{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Image",
			APIVersion: configv1.GroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: "cluster",
		},
	}
	if addRegistryCert {
		clusterImageConfig.Spec.AdditionalTrustedCA.Name = registryCertConfigMapName
	}
	if len(insecureRegistries) > 0 {
		clusterImageConfig.Spec.RegistrySources.InsecureRegistries = insecureRegistries
	}
	return json.Marshal(clusterImageConfig)
}
