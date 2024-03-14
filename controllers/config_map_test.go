package controllers_test

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go/aws/arn"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	capa "sigs.k8s.io/cluster-api-provider-aws/v2/api/v1beta2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/giantswarm/aws-crossplane-cluster-config-operator/controllers"
)

var _ = Describe("PrefixListEntryReconciler", func() {
	var (
		ctx context.Context

		accountID string
		identity  *capa.AWSClusterRoleIdentity
		cluster   *capa.AWSCluster

		request    ctrl.Request
		reconciler *controllers.ConfigMapReconciler
	)

	verifyConfigMap := func() {
		configMap := &corev1.ConfigMap{}
		err := k8sClient.Get(ctx, types.NamespacedName{
			Namespace: cluster.Namespace,
			Name:      fmt.Sprintf("%s-crossplane-config", cluster.Name),
		}, configMap)
		Expect(err).NotTo(HaveOccurred())
		Expect(configMap.Data).To(HaveKeyWithValue("values", MatchYAML(fmt.Sprintf(`
                accountID: "%s"
                baseDomain: %s.base.domain.io
                clusterName: %s
            `, accountID, cluster.Name, cluster.Name))))
	}

	verifyProviderConfig := func() {
		providerConfig := &unstructured.Unstructured{}
		providerConfig.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "aws.upbound.io",
			Kind:    "ProviderConfig",
			Version: "v1beta1",
		})

		err := k8sClient.Get(ctx, types.NamespacedName{
			Namespace: cluster.Namespace,
			Name:      cluster.Name,
		}, providerConfig)
		Expect(err).NotTo(HaveOccurred())

		Expect(providerConfig.Object).To(HaveKeyWithValue("metadata", MatchKeys(IgnoreExtras, Keys{
			"name": Equal(cluster.Name),
		})))
		Expect(providerConfig.Object).To(HaveKeyWithValue("spec", MatchKeys(IgnoreExtras, Keys{
			"credentials": MatchKeys(IgnoreExtras, Keys{
				"source": Equal("WebIdentity"),
				"webIdentity": MatchKeys(IgnoreExtras, Keys{
					"roleARN": Equal(fmt.Sprintf("arn:aws:iam::%s:role/the-assume-role", accountID)),
				}),
			}),
			"assumeRoleChain": ConsistOf(MatchKeys(IgnoreExtras, Keys{
				"roleARN": Equal(fmt.Sprintf("arn:aws:iam::%s:role/the-provider-role", accountID)),
			})),
		})))
	}

	BeforeEach(func() {
		ctx = context.Background()

		identity, cluster = createRandomClusterWithIdentity()
		reconciler = &controllers.ConfigMapReconciler{
			Client:       k8sClient,
			BaseDomain:   "base.domain.io",
			AssumeRole:   "the-assume-role",
			ProviderRole: "the-provider-role",
		}
		roleARN, err := arn.Parse(identity.Spec.RoleArn)
		Expect(err).NotTo(HaveOccurred())
		accountID = roleARN.AccountID

		request = ctrl.Request{
			NamespacedName: types.NamespacedName{
				Namespace: cluster.Namespace,
				Name:      cluster.Name,
			},
		}
	})

	AfterEach(func() {
		err := k8sClient.Delete(ctx, cluster)
		if k8serrors.IsNotFound(err) {
			return
		}
		Expect(err).NotTo(HaveOccurred())
	})

	JustBeforeEach(func() {
		result, err := reconciler.Reconcile(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Requeue).To(BeFalse())
	})

	It("creates the configmap", func() {
		verifyConfigMap()
	})

	It("creates the provider config", func() {
		verifyProviderConfig()
	})

	When("the account id changes", func() {
		BeforeEach(func() {
			someOtherAccount := "1234567"
			configMap := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: cluster.Namespace,
					Name:      fmt.Sprintf("%s-crossplane-config", cluster.Name),
				},
			}
			configMap.Data = map[string]string{
				"values": fmt.Sprintf(`
					"accountID":   "%s",
					"baseDomain":  %s.base.domain.io,
					"clusterName": %s,
                `, someOtherAccount, cluster.Name, cluster.Name),
			}
			err := k8sClient.Create(ctx, configMap)
			Expect(err).NotTo(HaveOccurred())

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
							"roleARN": fmt.Sprintf("arn:aws:iam::%s:role/some-other-assume-role", someOtherAccount),
						},
					},
					"assumeRoleChain": []map[string]interface{}{
						{
							"roleARN": fmt.Sprintf("arn:aws:iam::%s:role/some-other-provider-role", someOtherAccount),
						},
					},
				},
			}
			providerConfig.SetGroupVersionKind(schema.GroupVersionKind{
				Group:   "aws.upbound.io",
				Kind:    "ProviderConfig",
				Version: "v1beta1",
			})
			err = k8sClient.Create(ctx, providerConfig)
			Expect(err).NotTo(HaveOccurred())
		})

		It("updates the configmap", func() {
			verifyConfigMap()
		})

		It("updates the provider config", func() {
			verifyProviderConfig()
		})
	})

	When("the cluster is deleted", func() {
		BeforeEach(func() {
			patchedCluster := cluster.DeepCopy()
			patchedCluster.Finalizers = []string{controllers.Finalizer}

			err := k8sClient.Patch(context.Background(), patchedCluster, client.MergeFrom(cluster))
			Expect(err).NotTo(HaveOccurred())

			err = k8sClient.Delete(context.Background(), cluster)
			Expect(err).NotTo(HaveOccurred())
		})

		It("removes the finalizer", func() {
			err := k8sClient.Get(ctx, types.NamespacedName{
				Namespace: cluster.Namespace,
				Name:      cluster.Name,
			}, cluster)
			Expect(k8serrors.IsNotFound(err)).To(BeTrue())
		})

		It("removes the config map", func() {
			configMap := &corev1.ConfigMap{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Namespace: cluster.Namespace,
				Name:      fmt.Sprintf("%s-crossplane-config", cluster.Name),
			}, configMap)
			Expect(k8serrors.IsNotFound(err)).To(BeTrue())
		})

		It("removes the providerconfig", func() {
			providerConfig := &unstructured.Unstructured{}
			providerConfig.SetGroupVersionKind(schema.GroupVersionKind{
				Group:   "aws.upbound.io",
				Kind:    "ProviderConfig",
				Version: "v1beta1",
			})

			err := k8sClient.Get(ctx, types.NamespacedName{
				Namespace: cluster.Namespace,
				Name:      cluster.Name,
			}, providerConfig)
			Expect(k8serrors.IsNotFound(err)).To(BeTrue())
		})
	})

	When("the role arn is invalid", func() {
		It("returns an error", func() {
			identity.Spec.RoleArn = "invalid-arn"
			err := k8sClient.Update(ctx, identity)
			Expect(err).NotTo(HaveOccurred())

			_, err = reconciler.Reconcile(ctx, request)
			Expect(err).To(HaveOccurred())
		})
	})
})
