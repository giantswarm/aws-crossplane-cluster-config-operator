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

// SetupWithManager sets up the controller with the Manager.
func (r *ConfigMapReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&capa.AWSCluster{}).
		Complete(r)
}

func (r *ConfigMapReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	cluster := &capa.AWSCluster{}
	err := r.Client.Get(ctx, req.NamespacedName, cluster)
	if err != nil {
		logger.Error(err, "failed to get cluster")
		return ctrl.Result{}, errors.WithStack(client.IgnoreNotFound(err))
	}

	if !cluster.DeletionTimestamp.IsZero() {
		logger.Info("Reconciling delete")
		return r.reconcileDelete(ctx, cluster)
	}

	return r.reconcileNormal(ctx, cluster)
}

func (r *ConfigMapReconciler) reconcileNormal(ctx context.Context, cluster *capa.AWSCluster) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Reconciling")
	defer logger.Info("Done reconciling")

	identity := &capa.AWSClusterRoleIdentity{}
	err := r.Client.Get(
		ctx,
		types.NamespacedName{
			Name:      cluster.Spec.IdentityRef.Name,
			Namespace: cluster.Namespace,
		},
		identity,
	)
	if err != nil {
		logger.Error(err, "failed to get cluster role identity")
		return ctrl.Result{}, errors.WithStack(err)
	}

	roleARN, err := arn.Parse(identity.Spec.RoleArn)
	if err != nil {
		logger.Error(err, "failed to parse role arn")
		return ctrl.Result{}, errors.WithStack(err)
	}

	err = r.AddFinalizer(ctx, cluster)
	if err != nil {
		logger.Error(err, "failed to add finalizer")
		return ctrl.Result{}, errors.WithStack(err)
	}

	err = r.reconcileConfigMap(ctx, cluster, roleARN.AccountID, r.BaseDomain)
	if err != nil {
		logger.Error(err, "failed to reconcile config map")
		return ctrl.Result{}, errors.WithStack(err)

	}

	err = r.reconcileProviderConfig(ctx, cluster, roleARN.AccountID)
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
	cluster *capa.AWSCluster,
	accountID, baseDomain string,
) error {
	config := &corev1.ConfigMap{}
	err := r.Client.Get(ctx,
		types.NamespacedName{
			Name:      fmt.Sprintf("%s-crossplane-config", cluster.Name),
			Namespace: cluster.Namespace,
		},
		config,
	)

	if k8serrors.IsNotFound(err) {
		return r.createConfigMap(ctx, cluster, accountID, baseDomain)
	}

	return r.updateConfigMap(ctx, cluster, config, accountID, baseDomain)
}

func (r *ConfigMapReconciler) reconcileProviderConfig(ctx context.Context, cluster *capa.AWSCluster, accountID string) error {
	logger := log.FromContext(ctx)

	providerConfig := getProviderConfig(cluster)

	err := r.Client.Get(ctx, client.ObjectKeyFromObject(cluster), providerConfig)
	if metaerr.IsNoMatchError(err) {
		logger.Info("Provider config CRD not found, skipping provider config creation")
		return nil
	}
	if k8serrors.IsNotFound(err) {
		return r.createProviderConfig(ctx, providerConfig, accountID, cluster.Spec.Region)
	}
	if err != nil {
		logger.Error(err, "Failed to get provider config")
		return errors.WithStack(err)
	}

	return r.updateProviderConfig(ctx, providerConfig, accountID, cluster.Spec.Region)
}

func (r *ConfigMapReconciler) reconcileDelete(ctx context.Context, cluster *capa.AWSCluster) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Reconcile delete")
	defer logger.Info("Done deleting")

	config := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-crossplane-config", cluster.Name),
			Namespace: cluster.Namespace,
		},
	}

	err := r.Client.Delete(ctx, config)
	if err != nil && !k8serrors.IsNotFound(err) {
		logger.Error(err, "failed to delete config map")
		return ctrl.Result{}, errors.WithStack(err)
	}

	providerConfig := getProviderConfig(cluster)

	err = r.Client.Delete(ctx, providerConfig)
	if err != nil &&
		!k8serrors.IsNotFound(err) &&
		!metaerr.IsNoMatchError(err) {

		logger.Error(err, "failed to delete provider config")
		return ctrl.Result{}, errors.WithStack(err)
	}

	err = r.RemoveFinalizer(ctx, cluster)
	if err != nil {
		logger.Error(err, "failed to remove finalizer")
		return ctrl.Result{}, errors.WithStack(err)
	}

	return ctrl.Result{}, nil
}

func (r *ConfigMapReconciler) AddFinalizer(ctx context.Context, awsCluster *capa.AWSCluster) error {
	originalCluster := awsCluster.DeepCopy()
	controllerutil.AddFinalizer(awsCluster, Finalizer)
	return r.Client.Patch(ctx, awsCluster, client.MergeFrom(originalCluster))
}

func (r *ConfigMapReconciler) RemoveFinalizer(ctx context.Context, awsCluster *capa.AWSCluster) error {
	originalCluster := awsCluster.DeepCopy()
	controllerutil.RemoveFinalizer(awsCluster, Finalizer)
	return r.Client.Patch(ctx, awsCluster, client.MergeFrom(originalCluster))
}

func (r *ConfigMapReconciler) createConfigMap(ctx context.Context, cluster *capa.AWSCluster, accountID, baseDomain string) error {
	logger := log.FromContext(ctx)

	logger.Info("Creating config map")
	configMapValues, err := getConfigMapValues(cluster, accountID, baseDomain)
	if err != nil {
		return errors.WithStack(err)
	}

	config := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-crossplane-config", cluster.Name),
			Namespace: cluster.Namespace,
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
	cluster *capa.AWSCluster,
	config *corev1.ConfigMap,
	accountID, baseDomain string,
) error {
	logger := log.FromContext(ctx)

	configMapValues, err := getConfigMapValues(cluster, accountID, baseDomain)
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

func getConfigMapValues(cluster *capa.AWSCluster, accountID, baseDomain string) (string, error) {
	valuesAWSCluster := crossplaneConfigValuesAWSCluster{}
	if cluster.Spec.NetworkSpec.VPC.ID != "" {
		valuesAWSCluster.VpcID = cluster.Spec.NetworkSpec.VPC.ID
	}
	if sg, ok := cluster.Status.Network.SecurityGroups[capa.SecurityGroupControlPlane]; ok {
		if valuesAWSCluster.SecurityGroups == nil {
			valuesAWSCluster.SecurityGroups = &crossplaneConfigValuesAWSClusterSecurityGroups{}
		}
		valuesAWSCluster.SecurityGroups.ControlPlane = &crossplaneConfigValuesAWSClusterSecurityGroup{
			ID: sg.ID,
		}
	}

	values := crossplaneConfigValues{
		AccountID:    accountID,
		AWSCluster:   valuesAWSCluster,
		AWSPartition: getPartition(cluster.Spec.Region),
		BaseDomain:   fmt.Sprintf("%s.%s", cluster.Name, baseDomain),
		ClusterName:  cluster.Name,
		Region:       cluster.Spec.Region,
	}

	configMapValues, err := yaml.Marshal(values)
	if err != nil {
		return "", errors.WithStack(err)
	}

	return string(configMapValues), nil
}

func getProviderConfig(cluster *capa.AWSCluster) *unstructured.Unstructured {
	providerConfig := &unstructured.Unstructured{}
	providerConfig.Object = map[string]interface{}{
		"metadata": map[string]interface{}{
			"name":      cluster.Name,
			"namespace": cluster.Namespace,
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
