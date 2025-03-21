## Summary
ClusterImagePolicy and ImagePolicy CRD are managed by ContainerRuntimeConfig controller. These CRD allows setting up configurations for CRI-O to verify the container images signed using [Sigstore](https://www.sigstore.dev/) tools.

## Goals
Generating corresponding CRI-O configuration files for image signature verification. Rollout ClusterImagePolicy to `/etc/containers/policy.json` for cluster wide configuration. Rollout ImagePolicy to `/etc/crio/policies/<NAMESPACE>.json`. Roll out the registries configuration to `/etc/containers/registries.d/sigstore-registries.yaml`.

## CRD
[ClusterImagePolicy CRD](https://github.com/openshift/api/blob/master/config/v1alpha1/zz_generated.crd-manifests/0000_10_config-operator_01_clusterimagepolicies-TechPreviewNoUpgrade.crd.yaml)

[ImagePolicy CRD](https://github.com/openshift/api/blob/master/config/v1alpha1/zz_generated.crd-manifests/0000_10_config-operator_01_imagepolicies-TechPreviewNoUpgrade.crd.yaml)

## Example

Below is an example of a ClusterImagePolicy CRD.

```yaml
apiVersion: config.openshift.io/v1alpha1
kind: ClusterImagePolicy 
metadata:
  name: p0
spec:
  scopes:
    - registry.ci.openshift.org
    - example.com/global
  policy:
    rootOfTrust:
      policyType: PublicKey
      publicKey:
        keyData: Zm9vIGJhcg==
    signedIdentity:
      matchPolicy: MatchRepoDigestOrExact
```

Save the above clusterimagepolicy locally, for example as pubKeyPolicy.yaml.
Now apply the clusterimagepolicy that you created:

```shell
oc apply -f pubKeyPolicy.yaml
```

Check that it was created:

```shell
oc get clusterimagepolicy
NAME   AGE
p0     9s

```

## Validation and Troubleshooting
The machine-config-controller logs will show the error message if the ClusterImagePolicy/ImagePolicy and Image CR has conflicting configurations. Controller will fail to roll out the CR. 
- if blocked registries configured by Image CR exist, the clusterimagepolicy scopes must not equal to or nested under blockedRegistries
- if allowed registries configured by Image CR exist, the clusterimagepolicy/imagepolicy scopes nested under the allowedRegistries
For example, the below error message is shown when the ClusterImagePolicy and blockedRegistries of Image CR has conflicting configurations.

```shell
I0204 03:51:18.253145       1 container_runtime_config_controller.go:497] Error syncing image config openshift-config: could not Create/Update MachineConfig: could not update policy json with new changes: clusterimagePolicy configured for the scope example.com/global is nested inside blockedRegistries ```
```

The ImagePolicy CRD will have a status field that will show the message if the ImagePolicy and ClusterImagePolicy has conflicting scope configurations.

```yaml
Status:
  Conditions:
	Last Transition Time:  2024-09-16T19:34:11Z
	Message:           	has conflicting scope(s) ["example.com/global/image"] that equal to or nest inside existing clusterimagepolicy, only policy from clusterimagepolicy scope(s) will be applied
	Observed Generation:   1
	Reason:            	ConflictScopes
	Status:            	False
	Type:              	Pending
```

## Implementation Details
The ContainerRuntimeConfigController would perform the following steps:

1. Validate ClusterImagePolicy and ImagePolicy objects scopes, and update the status of the ImagePolicy object if its scopes are equal to or nested within the ClusterImagePolicy scopes.

2. Render the current MachineConfigs (storage.files.contents[policy.json]) into the originalPolicyIgn

3. Serialize the cluster level policies to `policy.json`.

4. Serialize the namespace level policies to `/etc/crio/<NAMESPACE>.json`.

4. Add registries configuration to `sigstore-registries.yaml`. This configuration is used to specify the sigstore is being used as the image signature verification backend. 

5. Update the ignition file `/etc/containers/policy.json` within the `99-<pool>-generated-registries` MachineConfig.

6. Create or Update the ignition file `/etc/crio/<NAMESPACE>.json`, `/etc/containers/registries.d/sigstore-registries.yaml` within the `99-<pool>-generated-imagepolicies` MachineConfig. 

After deletion all of the ClusterImagePolicy instance the config will be reverted to the original policy.json.

## See Also
see **[containers-policy.json(5)](https://github.com/containers/image/blob/main/docs/containers-policy.json.5.md)**, **[containers-registries.d(5)](https://github.com/containers/image/blob/main/docs/containers-registries.d.5.md)**  for more information.


