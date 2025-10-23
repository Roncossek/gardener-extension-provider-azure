// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company and Gardener contributors
//
// SPDX-License-Identifier: Apache-2.0

package mutator

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	extensionswebhook "github.com/gardener/gardener/extensions/pkg/webhook"
	gardencorev1beta1 "github.com/gardener/gardener/pkg/apis/core/v1beta1"
	"github.com/gardener/gardener/pkg/utils"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/gardener/gardener-extension-provider-azure/pkg/apis/azure/helper"
	"github.com/gardener/gardener-extension-provider-azure/pkg/apis/azure/v1alpha1"
)

// NewNamespacedCloudProfileMutator returns a new instance of a NamespacedCloudProfile mutator.
func NewNamespacedCloudProfileMutator(mgr manager.Manager) extensionswebhook.Mutator {
	return &namespacedCloudProfile{
		client:  mgr.GetClient(),
		decoder: serializer.NewCodecFactory(mgr.GetScheme(), serializer.EnableStrict).UniversalDecoder(),
	}
}

type namespacedCloudProfile struct {
	client  client.Client
	decoder runtime.Decoder
}

// Mutate mutates the given NamespacedCloudProfile object.
func (p *namespacedCloudProfile) Mutate(_ context.Context, newObj, _ client.Object) error {
	profile, ok := newObj.(*gardencorev1beta1.NamespacedCloudProfile)
	if !ok {
		return fmt.Errorf("wrong object type %T", newObj)
	}

	if shouldSkipMutation(profile) {
		return nil
	}

	specConfig, statusConfig, err := p.decodeConfigs(profile)
	if err != nil {
		return err
	}

	// TODO(Roncossek): Remove TransformProviderConfigToParentFormat once all CloudProfiles have been migrated to use CapabilityFlavors and the Architecture fields are effectively forbidden or have been removed.
	uniformSpecConfig := helper.TransformProviderConfigToParentFormat(specConfig, profile.Status.CloudProfileSpec.MachineTypes, profile.Status.CloudProfileSpec.MachineCapabilities)

	statusConfig.MachineImages = mergeMachineImages(uniformSpecConfig.MachineImages, statusConfig.MachineImages)
	statusConfig.MachineTypes = mergeMachineTypes(uniformSpecConfig.MachineTypes, statusConfig.MachineTypes)

	return p.updateProfileStatus(profile, statusConfig)
}

func shouldSkipMutation(profile *gardencorev1beta1.NamespacedCloudProfile) bool {
	// Ignore NamespacedCloudProfiles being deleted and wait for core mutator to patch the status.
	return profile.DeletionTimestamp != nil ||
		profile.Generation != profile.Status.ObservedGeneration ||
		profile.Spec.ProviderConfig == nil ||
		profile.Status.CloudProfileSpec.ProviderConfig == nil
}

func (p *namespacedCloudProfile) decodeConfigs(profile *gardencorev1beta1.NamespacedCloudProfile) (*v1alpha1.CloudProfileConfig, *v1alpha1.CloudProfileConfig, error) {
	specConfig := &v1alpha1.CloudProfileConfig{}
	statusConfig := &v1alpha1.CloudProfileConfig{}
	if err := p.decodeProviderConfig(profile.Spec.ProviderConfig.Raw, specConfig, "spec"); err != nil {
		return nil, nil, err
	}
	if err := p.decodeProviderConfig(profile.Status.CloudProfileSpec.ProviderConfig.Raw, statusConfig, "status"); err != nil {
		return nil, nil, err
	}

	return specConfig, statusConfig, nil
}

func (p *namespacedCloudProfile) decodeProviderConfig(raw []byte, into *v1alpha1.CloudProfileConfig, configType string) error {
	if _, _, err := p.decoder.Decode(raw, nil, into); err != nil {
		return fmt.Errorf("could not decode providerConfig of %s: %w", configType, err)
	}
	return nil
}

func (p *namespacedCloudProfile) updateProfileStatus(profile *gardencorev1beta1.NamespacedCloudProfile, config *v1alpha1.CloudProfileConfig) error {
	modifiedStatusConfig, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("failed to marshal status config: %w", err)
	}
	profile.Status.CloudProfileSpec.ProviderConfig.Raw = modifiedStatusConfig
	return nil
}

func mergeMachineImages(specMachineImages, statusMachineImages []v1alpha1.MachineImages) []v1alpha1.MachineImages {
	specImages := utils.CreateMapFromSlice(specMachineImages, func(mi v1alpha1.MachineImages) string { return mi.Name })
	statusImages := utils.CreateMapFromSlice(statusMachineImages, func(mi v1alpha1.MachineImages) string { return mi.Name })
	for _, specMachineImage := range specImages {
		if _, exists := statusImages[specMachineImage.Name]; !exists {
			statusImages[specMachineImage.Name] = specMachineImage
		} else {
			// since multiple version entries can exist for the same version string
			mergedVersions := make([]v1alpha1.MachineImageVersion, 0, len(statusImages[specMachineImage.Name].Versions)+len(specImages[specMachineImage.Name].Versions))

			// Add all existing status versions
			mergedVersions = append(mergedVersions, statusImages[specMachineImage.Name].Versions...)

			// Add all spec versions
			mergedVersions = append(mergedVersions, specImages[specMachineImage.Name].Versions...)

			statusImages[specMachineImage.Name] = v1alpha1.MachineImages{
				Name:     specMachineImage.Name,
				Versions: mergedVersions,
			}
		}
	}
	return slices.Collect(maps.Values(statusImages))
}

func mergeMachineTypes(specMachineTypes, statusMachineTypes []v1alpha1.MachineType) []v1alpha1.MachineType {
	specTypes := utils.CreateMapFromSlice(specMachineTypes, func(mi v1alpha1.MachineType) string { return mi.Name })
	statusTypes := utils.CreateMapFromSlice(statusMachineTypes, func(mi v1alpha1.MachineType) string { return mi.Name })
	for _, specMachineType := range specTypes {
		if _, exists := statusTypes[specMachineType.Name]; !exists {
			statusTypes[specMachineType.Name] = specMachineType
		}
	}
	return slices.Collect(maps.Values(statusTypes))
}
