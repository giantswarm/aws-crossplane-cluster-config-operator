/*
Copyright 2023.

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

package controllers

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go/aws/arn"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metaerr "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	capa "sigs.k8s.io/cluster-api-provider-aws/v2/api/v1beta2"
	eks "sigs.k8s.io/cluster-api-provider-aws/v2/controlplane/eks/api/v1beta2"
	capi "sigs.k8s.io/cluster-api/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/yaml"
)

const Finalizer = "crossplane-config-operator.finalizers.giantswarm.io/config-map-controller"

type ConfigMapReconciler struct {
	Client       client.Client
	BaseDomain   string
	ProviderRole string
	AssumeRole   string
}

type ClusterInfo struct {
	Name           string
	Namespace      string
	Region         string
	AWSPartition   string
	VpcID          string
	RoleArn        arn.ARN
	SecurityGroups *crossplaneConfigValuesAWSClusterSecurityGroups
}

// SetupWithManager sets up the controller with the Manager.
func (r *ConfigMapReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&capi.Cluster{}).
		Complete(r)
}

func (r *ConfigMapReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	awsCluster := &capa.AWSCluster{}
	ekscontrolplane := &eks.AWSManagedControlPlane{}
	clusterInfo := &ClusterInfo{}

	capiCluster := &capi.Cluster{}
	err := r.Client.Get(ctx, req.NamespacedName, capiCluster)

	if err != nil {
		logger.Error(err, "failed to get cluster")
		return ctrl.Result{}, errors.WithStack(client.IgnoreNotFound(err))
	}

	if !capiCluster.DeletionTimestamp.IsZero() {
		logger.Info("Reconciling delete")
		return r.reconcileDelete(ctx, capiCluster)
	}

	err = r.Client.Get(ctx, req.NamespacedName, awsCluster)
	if err == nil {
		clusterInfo.Name = awsCluster.Name
		clusterInfo.Namespace = awsCluster.Namespace
		clusterInfo.Region = awsCluster.Spec.Region
		clusterInfo.AWSPartition = getPartition(clusterInfo.Region)
		clusterInfo.VpcID = awsCluster.Spec.NetworkSpec.VPC.ID
		clusterInfo.RoleArn, err = r.getRoleArn(ctx, awsCluster.Spec.IdentityRef.Name, awsCluster.Namespace)
		if sg, ok := awsCluster.Status.Network.SecurityGroups[capa.SecurityGroupControlPlane]; ok {
			if clusterInfo.SecurityGroups == nil {
				clusterInfo.SecurityGroups = &crossplaneConfigValuesAWSClusterSecurityGroups{}
			}
			clusterInfo.SecurityGroups.ControlPlane = &crossplaneConfigValuesAWSClusterSecurityGroup{
				ID: sg.ID,
			}
		}
		if err != nil {
			logger.Error(err, "failed to get cluster role identity")
			return ctrl.Result{}, errors.WithStack(client.IgnoreNotFound(err))
		}
	} else {
		if k8serrors.IsNotFound(err) {
			err := r.Client.Get(ctx, req.NamespacedName, ekscontrolplane)
			if err != nil {
				logger.Error(err, "failed to get cluster")
				return ctrl.Result{}, errors.WithStack(client.IgnoreNotFound(err))
			}

			clusterInfo.Name = ekscontrolplane.Name
			clusterInfo.Namespace = ekscontrolplane.Namespace
			clusterInfo.Region = ekscontrolplane.Spec.Region
			clusterInfo.AWSPartition = getPartition(clusterInfo.Region)
			clusterInfo.VpcID = ekscontrolplane.Spec.NetworkSpec.VPC.ID
			clusterInfo.RoleArn, err = r.getRoleArn(ctx, ekscontrolplane.Spec.IdentityRef.Name, ekscontrolplane.Namespace)
			if err != nil {
				logger.Error(err, "failed to get cluster role identity")
				return ctrl.Result{}, errors.WithStack(client.IgnoreNotFound(err))
			}

		}

	}

	return r.reconcileNormal(ctx, clusterInfo)
}

func (r *ConfigMapReconciler) getRoleArn(ctx context.Context, idRef string, namespace string) (arn.ARN, error) {
	logger := log.FromContext(ctx)
	identity := &capa.AWSClusterRoleIdentity{}
	err := r.Client.Get(
		ctx,
		types.NamespacedName{
			Name:      idRef,
			Namespace: namespace,
		},
		identity,
	)
	if err != nil {
		return arn.ARN{}, errors.WithStack(err)
	}

	roleARN, err := arn.Parse(identity.Spec.RoleArn)
	if err != nil {
		logger.Error(err, "failed to parse role arn")
		return arn.ARN{}, errors.WithStack(err)
	}

	return roleARN, nil
}

func (r *ConfigMapReconciler) reconcileNormal(ctx context.Context, clusterInfo *ClusterInfo) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Reconciling")
	defer logger.Info("Done reconciling")

	capiCluster := &capi.Cluster{}
	err := r.Client.Get(ctx, types.NamespacedName{
		Name:      clusterInfo.Name,
		Namespace: clusterInfo.Namespace,
	}, capiCluster)
	if err != nil {
		logger.Error(err, "failed to get cluster")
		return ctrl.Result{}, errors.WithStack(client.IgnoreNotFound(err))
	}

	err = r.AddFinalizer(ctx, capiCluster)
	if err != nil {
		logger.Error(err, "failed to add finalizer")
		return ctrl.Result{}, errors.WithStack(err)
	}

	err = r.reconcileConfigMap(ctx, clusterInfo, clusterInfo.RoleArn.AccountID, r.BaseDomain)
	if err != nil {
		logger.Error(err, "failed to reconcile config map")
		return ctrl.Result{}, errors.WithStack(err)

	}

	err = r.reconcileProviderConfig(ctx, clusterInfo, clusterInfo.RoleArn.AccountID)
	if err != nil {
		logger.Error(err, "failed to reconcile provider config")
		return ctrl.Result{}, errors.WithStack(err)

	}

	return ctrl.Result{}, nil
}

type crossplaneConfigValues struct {
	AccountID    string                           `json:"accountID"`
	AWSCluster   crossplaneConfigValuesAWSCluster `json:"awsCluster"`
	AWSPartition string                           `json:"awsPartition"`
	BaseDomain   string                           `json:"baseDomain"`
	ClusterName  string                           `json:"clusterName"`
	Region       string                           `json:"region"`
}

type crossplaneConfigValuesAWSCluster struct {
	// Filled once available
	VpcID          string                                          `json:"vpcId,omitempty"`
	SecurityGroups *crossplaneConfigValuesAWSClusterSecurityGroups `json:"securityGroups,omitempty"`
}

type crossplaneConfigValuesAWSClusterSecurityGroups struct {
	// Filled once available
	ControlPlane *crossplaneConfigValuesAWSClusterSecurityGroup `json:"controlPlane,omitempty"`
}

type crossplaneConfigValuesAWSClusterSecurityGroup struct {
	ID string `json:"id"`
}

func (r *ConfigMapReconciler) reconcileConfigMap(
	ctx context.Context,
	clusterInfo *ClusterInfo,
	accountID, baseDomain string,
) error {
	config := &corev1.ConfigMap{}
	err := r.Client.Get(ctx,
		types.NamespacedName{
			Name:      fmt.Sprintf("%s-crossplane-config", clusterInfo.Name),
			Namespace: clusterInfo.Namespace,
		},
		config,
	)

	if k8serrors.IsNotFound(err) {
		return r.createConfigMap(ctx, clusterInfo, accountID, baseDomain)
	}

	return r.updateConfigMap(ctx, clusterInfo, config, accountID, baseDomain)
}

func (r *ConfigMapReconciler) reconcileProviderConfig(ctx context.Context, clusterInfo *ClusterInfo, accountID string) error {
	logger := log.FromContext(ctx)

	providerConfig := getProviderConfig(clusterInfo.Name, clusterInfo.Namespace)

	err := r.Client.Get(ctx, types.NamespacedName{
		Name:      clusterInfo.Name,
		Namespace: clusterInfo.Namespace,
	}, providerConfig)
	if metaerr.IsNoMatchError(err) {
		logger.Info("Provider config CRD not found, skipping provider config creation")
		return nil
	}
	if k8serrors.IsNotFound(err) {
		return r.createProviderConfig(ctx, providerConfig, accountID, clusterInfo.Region)
	}
	if err != nil {
		logger.Error(err, "Failed to get provider config")
		return errors.WithStack(err)
	}

	return r.updateProviderConfig(ctx, providerConfig, accountID, clusterInfo.Region)
}

func (r *ConfigMapReconciler) reconcileDelete(ctx context.Context, capiCluster *capi.Cluster) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Reconcile delete")
	defer logger.Info("Done deleting")

	config := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-crossplane-config", capiCluster.Name),
			Namespace: capiCluster.Namespace,
		},
	}

	logger.Info("Deleting ConfigMap")
	err := r.Client.Delete(ctx, config)
	if err != nil && !k8serrors.IsNotFound(err) {
		logger.Error(err, "failed to delete config map")
		return ctrl.Result{}, errors.WithStack(err)
	}

	providerConfig := getProviderConfig(capiCluster.Name, capiCluster.Namespace)
	logger.Info("Deleting ProviderConfig")
	err = r.Client.Delete(ctx, providerConfig)
	if err != nil &&
		!k8serrors.IsNotFound(err) &&
		!metaerr.IsNoMatchError(err) {

		logger.Error(err, "failed to delete provider config")
		return ctrl.Result{}, errors.WithStack(err)
	}

	logger.Info("Removing Finalizer")
	err = r.RemoveFinalizer(ctx, capiCluster)
	if err != nil {
		logger.Error(err, "failed to remove finalizer")
		return ctrl.Result{}, errors.WithStack(err)
	}

	return ctrl.Result{}, nil
}

func (r *ConfigMapReconciler) AddFinalizer(ctx context.Context, capiCluster *capi.Cluster) error {
	originalCluster := capiCluster.DeepCopy()
	controllerutil.AddFinalizer(capiCluster, Finalizer)
	return r.Client.Patch(ctx, capiCluster, client.MergeFrom(originalCluster))
}

func (r *ConfigMapReconciler) RemoveFinalizer(ctx context.Context, capiCluster *capi.Cluster) error {
	originalCluster := capiCluster.DeepCopy()
	controllerutil.RemoveFinalizer(capiCluster, Finalizer)
	error := r.Client.Patch(ctx, capiCluster, client.MergeFrom(originalCluster))

	// //check if there is an AWSCluster with the same name and namespace as the capiCluster and remove the finalizer from it as we are
	// awsCluster := &capa.AWSCluster{}
	// err := r.Client.Get(ctx, types.NamespacedName{
	// 	Name:      capiCluster.Name,
	// 	Namespace: capiCluster.Namespace,
	// }, awsCluster)
	// if err == nil {
	// 	originalAWSCluster := awsCluster.DeepCopy()
	// 	controllerutil.RemoveFinalizer(awsCluster, Finalizer)
	// 	err = r.Client.Patch(ctx, awsCluster, client.MergeFrom(originalAWSCluster))
	// 	if err != nil {
	// 		return errors.WithStack(err)
	// 	}
	// }

	return error
}

func (r *ConfigMapReconciler) createConfigMap(ctx context.Context, clusterInfo *ClusterInfo, accountID, baseDomain string) error {
	logger := log.FromContext(ctx)

	logger.Info("Creating config map")
	configMapValues, err := getConfigMapValues(clusterInfo, accountID, baseDomain)
	if err != nil {
		return errors.WithStack(err)
	}

	config := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-crossplane-config", clusterInfo.Name),
			Namespace: clusterInfo.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "aws-crossplane-cluster-config-operator",
			},
		},
		Data: map[string]string{
			"values": configMapValues,
		},
	}

	err = r.Client.Create(ctx, config)
	if k8serrors.IsAlreadyExists(err) {
		logger.Info("config map already exists")
		return errors.WithStack(err)
	}
	if err != nil {
		logger.Error(err, "failed to create config map")
		return errors.WithStack(err)
	}

	return nil
}

func (r *ConfigMapReconciler) updateConfigMap(ctx context.Context,
	clusterInfo *ClusterInfo,
	config *corev1.ConfigMap,
	accountID, baseDomain string,
) error {
	logger := log.FromContext(ctx)

	configMapValues, err := getConfigMapValues(clusterInfo, accountID, baseDomain)
	if err != nil {
		return errors.WithStack(err)
	}
	patchedConfig := config.DeepCopy()
	patchedConfig.Data["values"] = configMapValues

	err = r.Client.Patch(ctx, patchedConfig, client.MergeFrom(config))
	if err != nil {
		logger.Error(err, "failed to patch config map")
		return errors.WithStack(err)
	}

	return nil
}

func (r *ConfigMapReconciler) createProviderConfig(ctx context.Context, providerConfig *unstructured.Unstructured, accountID, region string) error {
	logger := log.FromContext(ctx)

	providerConfig.Object["spec"] = r.getProviderConfigSpec(accountID, region)

	err := r.Client.Create(ctx, providerConfig)
	if k8serrors.IsAlreadyExists(err) {
		logger.Info("provider config already exists")
		return nil
	}
	if err != nil {
		logger.Error(err, "failed to create provider config")
		return errors.WithStack(err)
	}

	return nil
}

func (r *ConfigMapReconciler) updateProviderConfig(ctx context.Context, providerConfig *unstructured.Unstructured, accountID, region string) error {
	logger := log.FromContext(ctx)

	patchedConfig := providerConfig.DeepCopy()
	patchedConfig.Object["spec"] = r.getProviderConfigSpec(accountID, region)
	err := r.Client.Patch(ctx, patchedConfig, client.MergeFrom(providerConfig))
	if err != nil {
		logger.Error(err, "Failed to patch provider config")
		return errors.WithStack(err)
	}

	return nil
}

func (r *ConfigMapReconciler) getProviderConfigSpec(accountID, region string) map[string]interface{} {
	partition := getPartition(region)
	return map[string]interface{}{
		"credentials": map[string]interface{}{
			"source": "WebIdentity",
			"webIdentity": map[string]interface{}{
				"roleARN": fmt.Sprintf("arn:%s:iam::%s:role/%s", partition, accountID, r.AssumeRole),
			},
		},
		"assumeRoleChain": []map[string]interface{}{
			{
				"roleARN": fmt.Sprintf("arn:%s:iam::%s:role/%s", partition, accountID, r.ProviderRole),
			},
		},
	}
}

func getConfigMapValues(clusterInfo *ClusterInfo, accountID, baseDomain string) (string, error) {
	valuesAWSCluster := crossplaneConfigValuesAWSCluster{}
	valuesAWSCluster.VpcID = clusterInfo.VpcID
	valuesAWSCluster.SecurityGroups = clusterInfo.SecurityGroups

	values := crossplaneConfigValues{
		AccountID:    accountID,
		AWSCluster:   valuesAWSCluster,
		AWSPartition: clusterInfo.AWSPartition,
		BaseDomain:   fmt.Sprintf("%s.%s", clusterInfo.Name, baseDomain),
		ClusterName:  clusterInfo.Name,
		Region:       clusterInfo.Region,
	}

	configMapValues, err := yaml.Marshal(values)
	if err != nil {
		return "", errors.WithStack(err)
	}

	return string(configMapValues), nil
}

func getProviderConfig(name string, namespace string) *unstructured.Unstructured {
	providerConfig := &unstructured.Unstructured{}
	providerConfig.Object = map[string]interface{}{
		"metadata": map[string]interface{}{
			"name":      name,
			"namespace": namespace,
		},
	}
	providerConfig.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "aws.upbound.io",
		Kind:    "ProviderConfig",
		Version: "v1beta1",
	})

	return providerConfig
}

func getPartition(region string) string {
	if strings.HasPrefix(region, "cn-") {
		return "aws-cn"
	}
	return "aws"
}
