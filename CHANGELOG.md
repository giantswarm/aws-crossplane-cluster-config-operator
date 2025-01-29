# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Changed

- Dynamically calculate CAPI and CAPA versions from go cache, so that we use the right path when installing the CRDs during tests.
- Add the `node` security group id to the ConfigMap

## [0.3.0] - 2024-09-20

### Changed

- Configure the Crossplane `ProviderConfig` to use the CAPA controller role directly without going through a middle-man. For this to work, the CAPA controller role needs to have the correct trust policy granting access to the Crossplane providers ServiceAccount.
- Write out a value `oidcDomains` to the config map containing all service account issuer domains, as defined by the new `aws.giantswarm.io/irsa-trust-domains` annotation on the AWSCluster. The primary domain is still written to value `oidcDomain`.

## [0.2.1] - 2024-08-14

### Fixed

- Disable logger development mode to avoid panicking

## [0.2.0] - 2024-04-25

## Changed

- Move finalizer from AWSCluster to Cluster

### Added

- Add CAPA-created VPC and security group IDs for usage with the Cilium ENI mode feature (to add pod network security groups via Crossplane)
- Support for EKS AWSManagedControlPlane

## [0.1.1] - 2024-03-26

### Fixed

- Do not create ProviderConfig when custom resource does not exist.

## [0.1.0] - 2024-03-21

### Added

- Initial implementation.

[Unreleased]: https://github.com/giantswarm/aws-crossplane-cluster-config-operator/compare/v0.3.0...HEAD
[0.3.0]: https://github.com/giantswarm/aws-crossplane-cluster-config-operator/compare/v0.2.1...v0.3.0
[0.2.1]: https://github.com/giantswarm/aws-crossplane-cluster-config-operator/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/giantswarm/aws-crossplane-cluster-config-operator/compare/v0.1.1...v0.2.0
[0.1.1]: https://github.com/giantswarm/aws-crossplane-cluster-config-operator/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/giantswarm/aws-crossplane-cluster-config-operator/releases/tag/v0.1.0
