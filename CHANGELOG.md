# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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

[Unreleased]: https://github.com/giantswarm/aws-crossplane-cluster-config-operator/compare/v0.1.1...HEAD
[0.1.1]: https://github.com/giantswarm/aws-crossplane-cluster-config-operator/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/giantswarm/aws-crossplane-cluster-config-operator/releases/tag/v0.1.0
