/*
Copyright 2022.

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

package controllers_test

import (
	"context"
	"fmt"
	"go/build"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-logr/logr"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.uber.org/zap/zapcore"
	"golang.org/x/tools/go/packages"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/kubectl/pkg/scheme"
	capa "sigs.k8s.io/cluster-api-provider-aws/v2/api/v1beta2"
	eks "sigs.k8s.io/cluster-api-provider-aws/v2/controlplane/eks/api/v1beta2"
	capi "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/giantswarm/aws-crossplane-cluster-config-operator/tests"
	// +kubebuilder:scaffold:imports
)

// These tests use Ginkgo (BDD-style Go testing framework). Refer to
// http://onsi.github.io/ginkgo/ to learn more about Ginkgo.

func TestControllers(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Controller Suite")
}

var (
	logger    logr.Logger
	k8sClient client.Client
	testEnv   *envtest.Environment
	namespace string
)

var _ = BeforeSuite(func() {
	opts := zap.Options{
		DestWriter:  GinkgoWriter,
		Development: true,
		TimeEncoder: zapcore.RFC3339TimeEncoder,
	}
	logger = zap.New(zap.UseFlagOptions(&opts))
	logf.SetLogger(logger)

	tests.GetEnvOrSkip("KUBEBUILDER_ASSETS")

	// We need to calculate the cluster-api version to load the CRDs from the right path
	capiModule, err := packages.Load(&packages.Config{Mode: packages.NeedModule}, "sigs.k8s.io/cluster-api")
	Expect(err).NotTo(HaveOccurred())
	// We need to calculate the cluster-api-provider-aws version to load the CRDs from the right path
	capaModule, err := packages.Load(&packages.Config{Mode: packages.NeedModule}, "sigs.k8s.io/cluster-api-provider-aws/v2")
	Expect(err).NotTo(HaveOccurred())

	By("bootstrapping test environment")
	ex, err := os.Executable()
	Expect(err).NotTo(HaveOccurred())
	crdPath := filepath.Join(filepath.Dir(ex), "..", "tests", "testdata", "crds")
	testEnv = &envtest.Environment{
		CRDDirectoryPaths: []string{
			filepath.Join(build.Default.GOPATH, "pkg", "mod", "sigs.k8s.io", fmt.Sprintf("cluster-api@%s", capiModule[0].Module.Version), "config", "crd", "bases"),
			filepath.Join(build.Default.GOPATH, "pkg", "mod", "sigs.k8s.io", "cluster-api-provider-aws", fmt.Sprintf("v2@%s", capaModule[0].Module.Version), "config", "crd", "bases"),
			crdPath,
		},
		ErrorIfCRDPathMissing: true,
	}

	cfg, err := testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(cfg).NotTo(BeNil())

	err = capa.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	err = eks.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	err = capi.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())
	// +kubebuilder:scaffold:scheme

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient).NotTo(BeNil())
})

var _ = AfterSuite(func() {
	By("tearing down the test environment")
	if testEnv == nil {
		return
	}
	err := testEnv.Stop()
	Expect(err).NotTo(HaveOccurred())
})

var _ = BeforeEach(func() {
	namespace = uuid.New().String()
	namespaceObj := &corev1.Namespace{}
	namespaceObj.Name = namespace
	Expect(k8sClient.Create(context.Background(), namespaceObj)).To(Succeed())
})

var _ = AfterEach(func() {
	namespaceObj := &corev1.Namespace{}
	namespaceObj.Name = namespace
	Expect(k8sClient.Delete(context.Background(), namespaceObj)).To(Succeed())
})

func newCapiCluster(name string, annotationsKeyValues ...string) *capi.Cluster {
	if len(annotationsKeyValues)%2 != 0 {
		Fail("wrong number of arguments for newCluster. Expected even number of arguments for annotation key/value pairs")
	}

	annotations := map[string]string{}
	for i := 0; i < len(annotationsKeyValues); i += 2 {
		annotations[annotationsKeyValues[i]] = annotationsKeyValues[i+1]
	}

	awsCluster := &capi.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   namespace,
			Annotations: annotations,
		},
	}

	return awsCluster
}

func newCapaCluster(name string, annotationsKeyValues ...string) *capa.AWSCluster {
	if len(annotationsKeyValues)%2 != 0 {
		Fail("wrong number of arguments for newCluster. Expected even number of arguments for annotation key/value pairs")
	}

	annotations := map[string]string{}
	for i := 0; i < len(annotationsKeyValues); i += 2 {
		annotations[annotationsKeyValues[i]] = annotationsKeyValues[i+1]
	}

	awsCluster := &capa.AWSCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   namespace,
			Annotations: annotations,
		},
		Spec: capa.AWSClusterSpec{
			Region: "the-region",
			NetworkSpec: capa.NetworkSpec{
				VPC: capa.VPCSpec{
					ID:        "vpc-1",
					CidrBlock: fmt.Sprintf("10.%d.0.0/24", rand.Intn(255)),
				},
				Subnets: capa.Subnets{
					{
						ID:       "sub-1",
						IsPublic: false,
					},
				},
			},
		},
	}

	return awsCluster
}

func newEksCluster(name string, annotationsKeyValues ...string) *eks.AWSManagedControlPlane {
	if len(annotationsKeyValues)%2 != 0 {
		Fail("wrong number of arguments for newCluster. Expected even number of arguments for annotation key/value pairs")
	}

	annotations := map[string]string{}
	for i := 0; i < len(annotationsKeyValues); i += 2 {
		annotations[annotationsKeyValues[i]] = annotationsKeyValues[i+1]
	}

	eksCluster := &eks.AWSManagedControlPlane{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   namespace,
			Annotations: annotations,
		},
		Spec: eks.AWSManagedControlPlaneSpec{
			Region: "the-region",
			ControlPlaneEndpoint: capi.APIEndpoint{
				Host: "https://eks123clusterID.sk1.eu-west-2.eks.amazonaws.com",
				Port: 443,
			},
			NetworkSpec: capa.NetworkSpec{
				VPC: capa.VPCSpec{
					ID:        "vpc-1",
					CidrBlock: fmt.Sprintf("10.%d.0.0/24", rand.Intn(255)),
				},
				Subnets: capa.Subnets{
					{
						ID:       "sub-1",
						IsPublic: false,
					},
				},
			},
		},
	}

	return eksCluster
}

func createRandomCapaClusterWithIdentity(annotationsKeyValues ...string) (*capa.AWSClusterRoleIdentity, *capa.AWSCluster, *capi.Cluster) {
	name := uuid.NewString()
	awsCluster := newCapaCluster(name, annotationsKeyValues...)
	capiCluster := newCapiCluster(name, annotationsKeyValues...)
	identity := newRoleIdentity()

	awsCluster.Spec.IdentityRef = &capa.AWSIdentityReference{
		Name: identity.Name,
		Kind: "AWSClusterRoleIdentity",
	}

	Expect(k8sClient.Create(context.Background(), capiCluster)).To(Succeed())
	tests.PatchCAPIClusterStatus(k8sClient, capiCluster, capi.ClusterStatus{
		Phase: "Running",
	})

	Expect(k8sClient.Create(context.Background(), awsCluster)).To(Succeed())
	Expect(k8sClient.Create(context.Background(), identity)).To(Succeed())
	tests.PatchAWSClusterStatus(k8sClient, awsCluster, capa.AWSClusterStatus{
		Ready: true,
	})

	return identity, awsCluster, capiCluster
}

func createRandomAwsManagedControlplaneWithIdentity(annotationsKeyValues ...string) (*capa.AWSClusterRoleIdentity, *eks.AWSManagedControlPlane, *capi.Cluster) {
	name := uuid.NewString()
	eksCluster := newEksCluster(name, annotationsKeyValues...)
	capiCluster := newCapiCluster(name, annotationsKeyValues...)
	identity := newRoleIdentity()

	capiCluster.Spec.InfrastructureRef = &corev1.ObjectReference{
		Kind: "AWSManagedCluster",
	}
	capiCluster.Spec.ControlPlaneRef = &corev1.ObjectReference{
		Kind: "AWSManagedControlPlane",
	}

	eksCluster.Spec.IdentityRef = &capa.AWSIdentityReference{
		Name: identity.Name,
		Kind: "AWSClusterRoleIdentity",
	}

	Expect(k8sClient.Create(context.Background(), capiCluster)).To(Succeed())
	tests.PatchCAPIClusterStatus(k8sClient, capiCluster, capi.ClusterStatus{
		Phase: "Running",
	})

	Expect(k8sClient.Create(context.Background(), eksCluster)).To(Succeed())
	Expect(k8sClient.Create(context.Background(), identity)).To(Succeed())
	tests.PatchEKSClusterStatus(k8sClient, eksCluster, eks.AWSManagedControlPlaneStatus{
		Ready: true,
	})

	return identity, eksCluster, capiCluster
}

func newRoleIdentity() *capa.AWSClusterRoleIdentity {
	name := uuid.NewString()
	return &capa.AWSClusterRoleIdentity{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: capa.AWSClusterRoleIdentitySpec{
			AWSRoleSpec: capa.AWSRoleSpec{
				RoleArn: fmt.Sprintf("arn:aws:iam::%d:role/%s", rand.Intn(1000000), name),
			},
		},
	}
}
