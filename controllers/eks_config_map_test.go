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
	eks "sigs.k8s.io/cluster-api-provider-aws/v2/controlplane/eks/api/v1beta2"
	capi "sigs.k8s.io/cluster-api/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/giantswarm/aws-crossplane-cluster-config-operator/controllers"
)

var _ = Describe("ConfigMapReconcilerEKS", func() {
	var (
		ctx context.Context

		accountID       string
		identity        *capa.AWSClusterRoleIdentity
		awsManagedControlplane *eks.AWSManagedControlPlane
		cluster     *capi.Cluster

		request    ctrl.Request
		reconciler *controllers.ConfigMapReconciler
	)

	verifyConfigMap := func() {
		configMap := &corev1.ConfigMap{}
		err := k8sClient.Get(ctx, types.NamespacedName{
			Namespace: capiCluster.Namespace,
			Name:      fmt.Sprintf("%s-crossplane-config", capiCluster.Name),
		}, configMap)
		Expect(err).NotTo(HaveOccurred())
		Expect(configMap.Data).To(HaveKeyWithValue("values", MatchYAML(fmt.Sprintf(`
                accountID: "%s"
                awsCluster:
                  vpcId: vpc-1
                baseDomain: %s.base.domain.io
                clusterName: %s
                oidcDomain: oidc.eks.the-region.amazonaws.com/id/eks123clusterID
                region: the-region
                awsPartition: aws
            `, accountID, capiCluster.Name, capiCluster.Name))))
	}

	verifyProviderConfig := func() {
		providerConfig := &unstructured.Unstructured{}
		providerConfig.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "aws.upbound.io",
			Kind:    "ProviderConfig",
			Version: "v1beta1",
		})

		err := k8sClient.Get(ctx, types.NamespacedName{
			Namespace: capiCluster.Namespace,
			Name:      capiCluster.Name,
		}, providerConfig)
		Expect(err).NotTo(HaveOccurred())

		Expect(providerConfig.Object).To(HaveKeyWithValue("metadata", MatchKeys(IgnoreExtras, Keys{
			"name": Equal(capiCluster.Name),
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

		identity, ekscontrolplane, capiCluster = createRandomEKSControlPlaneWithIdentity()
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
				Namespace: ekscontrolplane.Namespace,
				Name:      ekscontrolplane.Name,
			},
		}
	})

	JustBeforeEach(func() {
		result, err := reconciler.Reconcile(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Requeue).To(BeFalse())
	})

	AfterEach(func() {
		err := k8sClient.Delete(ctx, capiCluster)
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
					Namespace: capiCluster.Namespace,
					Name:      fmt.Sprintf("%s-crossplane-config", capiCluster.Name),
				},
			}
			configMap.Data = map[string]string{
				"values": fmt.Sprintf(`
                    accountID: "%s"
                    awsCluster:
                        vpcId: vpc-1
                    awsPartition: cn
                    baseDomain: %s.base.domain.io
                    oidcDomain: oidc.eks.some-other-region.amazonaws.com.cn/id/eks123clusterID
                    clusterName: %s
                    region: some-other-region
                `, someOtherAccount, capiCluster.Name, capiCluster.Name),
			}
			err := k8sClient.Create(ctx, configMap)
			Expect(err).NotTo(HaveOccurred())

			providerConfig := &unstructured.Unstructured{}
			providerConfig.Object = map[string]interface{}{
				"metadata": map[string]interface{}{
					"name":      capiCluster.Name,
					"namespace": capiCluster.Namespace,
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
			patchedCluster := capiCluster.DeepCopy()
			patchedCluster.Finalizers = []string{controllers.Finalizer}

			err := k8sClient.Patch(context.Background(), patchedCluster, client.MergeFrom(capiCluster))
			Expect(err).NotTo(HaveOccurred())

			err = k8sClient.Delete(context.Background(), capiCluster)
			Expect(err).NotTo(HaveOccurred())
		})

		It("removes the finalizer", func() {
			err := k8sClient.Get(ctx, types.NamespacedName{
				Namespace: capiCluster.Namespace,
				Name:      capiCluster.Name,
			}, capiCluster)
			Expect(k8serrors.IsNotFound(err)).To(BeTrue())
		})

		It("removes the config map", func() {
			configMap := &corev1.ConfigMap{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Namespace: capiCluster.Namespace,
				Name:      fmt.Sprintf("%s-crossplane-config", capiCluster.Name),
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
				Namespace: capiCluster.Namespace,
				Name:      capiCluster.Name,
			}, providerConfig)
			Expect(k8serrors.IsNotFound(err)).To(BeTrue())
		})
	})

	When("the cluster is in china", func() {
		BeforeEach(func() {
			ekscontrolplane.Spec.Region = "cn-north-1"
			err := k8sClient.Update(ctx, ekscontrolplane)
			Expect(err).NotTo(HaveOccurred())
		})

		It("creates the configmap with the correct aws partition", func() {
			configMap := &corev1.ConfigMap{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Namespace: capiCluster.Namespace,
				Name:      fmt.Sprintf("%s-crossplane-config", capiCluster.Name),
			}, configMap)
			Expect(err).NotTo(HaveOccurred())
			Expect(configMap.Data).To(HaveKeyWithValue("values", MatchYAML(fmt.Sprintf(`
                accountID: "%s"
                awsCluster:
                  vpcId: vpc-1
                baseDomain: %s.base.domain.io
                oidcDomain: oidc.eks.cn-north-1.amazonaws.com.cn/id/eks123clusterID
                clusterName: %s
                region: cn-north-1
                awsPartition: aws-cn
            `, accountID, capiCluster.Name, capiCluster.Name))))
		})

		It("creates the provider config with the correct aws partition", func() {
			providerConfig := &unstructured.Unstructured{}
			providerConfig.SetGroupVersionKind(schema.GroupVersionKind{
				Group:   "aws.upbound.io",
				Kind:    "ProviderConfig",
				Version: "v1beta1",
			})

			err := k8sClient.Get(ctx, types.NamespacedName{
				Namespace: capiCluster.Namespace,
				Name:      capiCluster.Name,
			}, providerConfig)
			Expect(err).NotTo(HaveOccurred())

			Expect(providerConfig.Object).To(HaveKeyWithValue("metadata", MatchKeys(IgnoreExtras, Keys{
				"name": Equal(capiCluster.Name),
			})))
			Expect(providerConfig.Object).To(HaveKeyWithValue("spec", MatchKeys(IgnoreExtras, Keys{
				"credentials": MatchKeys(IgnoreExtras, Keys{
					"source": Equal("WebIdentity"),
					"webIdentity": MatchKeys(IgnoreExtras, Keys{
						"roleARN": Equal(fmt.Sprintf("arn:aws-cn:iam::%s:role/the-assume-role", accountID)),
					}),
				}),
				"assumeRoleChain": ConsistOf(MatchKeys(IgnoreExtras, Keys{
					"roleARN": Equal(fmt.Sprintf("arn:aws-cn:iam::%s:role/the-provider-role", accountID)),
				})),
			})))
		})
	})

	When("the cluster is provisioned by CAPA", func() {
		BeforeEach(func() {
			ekscontrolplane.Spec.NetworkSpec.VPC.ID = "vpc-123456"
			err := k8sClient.Update(ctx, ekscontrolplane)
			Expect(err).NotTo(HaveOccurred())

			ekscontrolplane.Status.Network.SecurityGroups = map[capa.SecurityGroupRole]capa.SecurityGroup{
				capa.SecurityGroupControlPlane: {
					ID: "sg-789987",
				},
			}
			err = k8sClient.Status().Update(ctx, ekscontrolplane)
			Expect(err).NotTo(HaveOccurred())
		})

		It("creates the configmap with the correct VPC ID and security group ID(s)", func() {
			configMap := &corev1.ConfigMap{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Namespace: capiCluster.Namespace,
				Name:      fmt.Sprintf("%s-crossplane-config", capiCluster.Name),
			}, configMap)
			Expect(err).NotTo(HaveOccurred())
			Expect(configMap.Data).To(HaveKeyWithValue("values", MatchYAML(fmt.Sprintf(`
                accountID: "%s"
                awsCluster:
                  vpcId: vpc-123456
                awsPartition: aws
                baseDomain: %s.base.domain.io
                oidcDomain: oidc.eks.the-region.amazonaws.com/id/eks123clusterID
                clusterName: %s
                region: the-region
            `, accountID, capiCluster.Name, capiCluster.Name))))
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
