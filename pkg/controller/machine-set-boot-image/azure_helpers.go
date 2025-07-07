package machineset

import (
	"fmt"
	"strings"

	machinev1beta1 "github.com/openshift/api/machine/v1beta1"
	corev1 "k8s.io/api/core/v1"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
)

func reconcileAzure(machineSet *machinev1beta1.MachineSet, configMap *corev1.ConfigMap, arch string, secretClient clientset.Interface) (patchRequired bool, newMachineSet *machinev1beta1.MachineSet, err error) {

	klog.Infof("Reconciling MAPI machineset %s on Azure, with arch %s", machineSet.Name, arch)

	// First, unmarshal the Azure providerSpec
	providerSpec := new(machinev1beta1.AzureMachineProviderSpec)
	if err := unmarshalProviderSpec(machineSet, providerSpec); err != nil {
		return false, nil, err
	}

	if providerSpec.Image.Type == machinev1beta1.AzureImageTypeMarketplaceWithPlan {
		// TODO: When adding support for paid marketplace plans, the variant checks will be slightly different
		// See https://issues.redhat.com/browse/MCO-1790
		klog.Infof("Skipping machineset %s, paid marketplace images cannot be updated via boot image updates", machineSet.Name)
		return false, nil, nil
	}

	if arch == "ppc64le" || arch == "s390x" {
		klog.Infof("Skipping machineset %s, machinesets with arch %s cannot be updated via boot image updates", machineSet.Name, arch)
		return false, nil, nil
	}

	/*
		// Next, unmarshal the configmap into a stream object
		streamData := new(stream.Stream)
		if err := unmarshalStreamDataConfigMap(configMap, streamData); err != nil {
			return false, nil, err
		}
	*/

	currentImage := providerSpec.Image
	// Machinesets that have a non empty resourceID are provisioning via an image that was
	// uploaded at install-time. For these cases, the MCO will transition them to unpaid
	// marketplace images. As part of https://issues.redhat.com//browse/CORS-3652, standard installs
	// will also begin to use the unpaid marketplace images or ARO images.
	usesLegacyImageUpload := (currentImage.ResourceID != "")

	// Create a hardcoded image for now, until the marketplace images are available in the coreos-bootimages configmap
	// TODO: For unpaid marketplace images, there are 3 variants: ARM, hyperGenV1 & hyperGenV2. This determination
	// can be done from the existing image information, and then used to pick from the stream data.
	newAzureImage := machinev1beta1.Image{
		Offer:      "aro4",
		Publisher:  "azureopenshift",
		ResourceID: "",
		SKU:        "419-v2",
		Type:       machinev1beta1.AzureImageTypeMarketplaceNoPlan,
		Version:    "419.6.20250523",
	}

	// Hacky way for now; this can be simplified once the marketplace streams are available in the configmap
	if arch == "aarch64" {
		newAzureImage.SKU = "419-arm"
	} else {
		var usesHyperVGen2 bool
		if usesLegacyImageUpload {
			usesHyperVGen2 = strings.Contains(currentImage.ResourceID, "gen2")
		} else {
			usesHyperVGen2 = strings.Contains(currentImage.SKU, "v2")
		}

		if usesHyperVGen2 {
			newAzureImage.SKU = "419"
		}
	}

	// If the current image matches, nothing to do here
	if currentImage.Version == newAzureImage.Version {
		return false, nil, nil
	}

	// Update the machine set with the new image
	newProviderSpec := providerSpec.DeepCopy()
	newProviderSpec.Image = newAzureImage

	newMachineSet = machineSet.DeepCopy()
	if err := marshalProviderSpec(newMachineSet, newProviderSpec); err != nil {
		return false, nil, fmt.Errorf("failed to update provider spec: %w", err)
	}

	return true, newMachineSet, nil
}
