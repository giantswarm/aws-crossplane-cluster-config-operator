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

	"github.com/aws/aws-sdk-go/aws/arn"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	capa "sigs.k8s.io/cluster-api-provider-aws/v2/api/v1beta2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const Finalizer = "crossplane-config-operator.finalizers.giantswarm.io/config-map-controller"

// ConfigMapReconciler reconciles a Frigate object
type ConfigMapReconciler struct {
	Client                client.Client
	ManagementClusterRole string
}

//+kubebuilder:rbac:groups=ship.my.domain,resources=frigates,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=ship.my.domain,resources=frigates/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=ship.my.domain,resources=frigates/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Frigate object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.11.2/pkg/reconcile
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

	err = r.reconcileConfigMap(ctx, cluster, roleARN.AccountID)
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

func (r *ConfigMapReconciler) reconcileConfigMap(
	ctx context.Context,
	cluster *capa.AWSCluster,
	accountID string,
) error {
	logger := log.FromContext(ctx)

	config := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-crossplane-config", cluster.Name),
			Namespace: cluster.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "aws-crossplane-cluster-config-operator",
			},
		},
		Data: map[string]string{
			"accountID":   accountID,
			"clusterName": cluster.Name,
		},
	}
	err := r.Client.Create(ctx, config)
	if k8serrors.IsAlreadyExists(err) {
		logger.Info("config map already exists")
		return nil
	}
	if err != nil {
		logger.Error(err, "failed to create config map")
		return errors.WithStack(err)
	}

	return nil
}

func (r *ConfigMapReconciler) reconcileProviderConfig(ctx context.Context, cluster *capa.AWSCluster, accountID string) error {
	logger := log.FromContext(ctx)

	// Using a unstructured object.
	providerConfig := &unstructured.Unstructured{}
	providerConfig.Object = map[string]interface{}{
		"metadata": map[string]interface{}{
			"name":      cluster.Name,
			"namespace": cluster.Namespace,
		},
		"spec": map[string]interface{}{
			"credentials": map[string]interface{}{
				"source": "WebIdentity",
				"webIdentity": map[string]interface{}{
					"roleARN": fmt.Sprintf("arn:aws:iam::%s:role/crossplane-assume-role", accountID),
				},
			},
			"assumeRoleChain": []map[string]interface{}{
				{
					"roleARN": fmt.Sprintf("arn:aws:iam::%s:role/%s", accountID, r.ManagementClusterRole),
				},
			},
		},
	}
	providerConfig.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "aws.upbound.io",
		Kind:    "ProviderConfig",
		Version: "v1beta1",
	})
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

	err = r.Client.Delete(ctx, providerConfig)
	if err != nil && !k8serrors.IsNotFound(err) {
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

// SetupWithManager sets up the controller with the Manager.
func (r *ConfigMapReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&capa.AWSCluster{}).
		Complete(r)
}
