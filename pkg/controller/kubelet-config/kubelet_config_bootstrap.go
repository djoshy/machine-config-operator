package kubeletconfig

import (
	"encoding/json"
	"fmt"

	configv1 "github.com/openshift/api/config/v1"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/version"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

// RunKubeletBootstrap generates MachineConfig objects for mcpPools that would have been generated by syncKubeletConfig
func RunKubeletBootstrap(templateDir string, kubeletConfigs []*mcfgv1.KubeletConfig, controllerConfig *mcfgv1.ControllerConfig, fgHandler ctrlcommon.FeatureGatesHandler, nodeConfig *configv1.Node, mcpPools []*mcfgv1.MachineConfigPool, apiServer *configv1.APIServer) ([]*mcfgv1.MachineConfig, error) {
	var res []*mcfgv1.MachineConfig
	managedKeyExist := make(map[string]bool)
	// Validate the KubeletConfig CR if exists
	for _, kubeletConfig := range kubeletConfigs {
		if err := validateUserKubeletConfig(kubeletConfig); err != nil {
			return nil, err
		}
	}
	if nodeConfig == nil {
		nodeConfig = createNewDefaultNodeconfig()
	}
	for _, kubeletConfig := range kubeletConfigs {
		// use selector since label matching part of a KubeletConfig is not handled during the bootstrap
		selector, err := metav1.LabelSelectorAsSelector(kubeletConfig.Spec.MachineConfigPoolSelector)
		if err != nil {
			return nil, fmt.Errorf("invalid label selector: %w", err)
		}

		for _, pool := range mcpPools {
			// If a pool with a nil or empty selector creeps in, it should match nothing, not everything.
			// skip the pool if no matched label for kubeletconfig
			if selector.Empty() || !selector.Matches(labels.Set(pool.Labels)) {
				continue
			}
			role := pool.Name

			originalKubeConfig, err := generateOriginalKubeletConfigWithFeatureGates(controllerConfig, templateDir, role, fgHandler, apiServer)
			if err != nil {
				return nil, err
			}
			// updating the originalKubeConfig based on the nodeConfig on a worker node
			if role == ctrlcommon.MachineConfigPoolWorker {
				updateOriginalKubeConfigwithNodeConfig(nodeConfig, originalKubeConfig)
			}
			if kubeletConfig.Spec.TLSSecurityProfile != nil {
				// Inject TLS Options from Spec
				observedMinTLSVersion, observedCipherSuites := ctrlcommon.GetSecurityProfileCiphers(kubeletConfig.Spec.TLSSecurityProfile)
				originalKubeConfig.TLSMinVersion = observedMinTLSVersion
				originalKubeConfig.TLSCipherSuites = observedCipherSuites
			}

			kubeletIgnition, logLevelIgnition, autoSizingReservedIgnition, err := generateKubeletIgnFiles(kubeletConfig, originalKubeConfig)
			if err != nil {
				return nil, err
			}

			tempIgnConfig := ctrlcommon.NewIgnConfig()
			if autoSizingReservedIgnition != nil {
				tempIgnConfig.Storage.Files = append(tempIgnConfig.Storage.Files, *autoSizingReservedIgnition)
			}
			if logLevelIgnition != nil {
				tempIgnConfig.Storage.Files = append(tempIgnConfig.Storage.Files, *logLevelIgnition)
			}
			if kubeletIgnition != nil {
				tempIgnConfig.Storage.Files = append(tempIgnConfig.Storage.Files, *kubeletIgnition)
			}

			rawIgn, err := json.Marshal(tempIgnConfig)
			if err != nil {
				return nil, err
			}
			managedKey, err := generateBootstrapManagedKeyKubelet(pool, managedKeyExist)
			if err != nil {
				return nil, err
			}
			// the first managed key value 99-poolname-generated-kubelet does not have a suffix
			// set "" as suffix annotation to the kubelet config object
			kubeletConfig.SetAnnotations(map[string]string{
				ctrlcommon.MCNameSuffixAnnotationKey: "",
			})
			ignConfig := ctrlcommon.NewIgnConfig()
			mc, err := ctrlcommon.MachineConfigFromIgnConfig(role, managedKey, ignConfig)
			if err != nil {
				return nil, fmt.Errorf("could not create MachineConfig from new Ignition config: %w", err)
			}
			mc.Spec.Config.Raw = rawIgn
			mc.SetAnnotations(map[string]string{
				ctrlcommon.GeneratedByControllerVersionAnnotationKey: version.Hash,
			})
			oref := metav1.OwnerReference{
				APIVersion: controllerKind.GroupVersion().String(),
				Kind:       controllerKind.Kind,
			}
			mc.SetOwnerReferences([]metav1.OwnerReference{oref})
			res = append(res, mc)
		}
	}
	return res, nil
}

// generateBootstrapManagedKeyKubelet generates the machine config name for a CR during bootstrap, returns error if there's more than 1 kubeletconfigs fir the same pool
// Note: Only one kubeletconfig manifest per pool is allowed for bootstrap mode for the following reason:
// if you provide multiple per pool, they would overwrite each other and not merge, potentially confusing customers post install;
// we can simplify the logic for the bootstrap generation and avoid some edge cases.
func generateBootstrapManagedKeyKubelet(pool *mcfgv1.MachineConfigPool, managedKeyExist map[string]bool) (string, error) {
	if _, ok := managedKeyExist[pool.Name]; ok {
		return "", fmt.Errorf("Error found multiple KubeletConfigs targeting MachineConfigPool %v. Please apply only one KubeletConfig manifest for each pool during installation", pool.Name)
	}
	managedKey, err := ctrlcommon.GetManagedKey(pool, nil, "99", "kubelet", "")
	if err != nil {
		return "", err
	}
	managedKeyExist[pool.Name] = true
	return managedKey, nil
}
