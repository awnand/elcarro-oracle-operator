// Copyright 2021 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package namespacetest

import (
	"context"
	"testing"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest/printer"

	// Enable GCP auth for k8s client
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"

	commonv1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/common/api/v1alpha1"
	v1alpha1 "github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/api/v1alpha1"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers/instancecontroller"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/controllers/testhelpers"
	"github.com/GoogleCloudPlatform/elcarro-oracle-operator/oracle/pkg/k8s"
)

// Made global to be accessible by AfterSuite
var k8sEnv = testhelpers.K8sOperatorEnvironment{}

// In case of Ctrl-C clean up the last valid k8sEnv.
var _ = AfterSuite(func() {
	k8sEnv.Close()
})

var _ = Describe("Instance and Database provisioning", func() {
	var operatorNamespace string
	var instanceNamespace string
	var instanceName string
	var cdbName string

	BeforeEach(func() {
		defer GinkgoRecover()
		operatorNamespace = testhelpers.RandName("namespace-test-op")
		instanceNamespace = testhelpers.RandName("namespace-test-inst")
		instanceName = "mydb-1"
		cdbName = "MYDB"

		k8sEnv.Init(operatorNamespace, instanceNamespace)
	})

	AfterEach(func() {
		if CurrentGinkgoTestDescription().Failed {
			testhelpers.PrintSimpleDebugInfo(k8sEnv, instanceName, cdbName)
		}
		k8sEnv.Close()
	})

	TestInstanceCreationAndDatabaseProvisioning := func(version string, edition string, extra string, isImageSeeded bool) {
		It("Should create instance and provision database", func() {
			ctx := context.Background()
			k8sClient, err := client.New(k8sEnv.Env.Config, client.Options{})
			Expect(err).ToNot(HaveOccurred())

			instanceTimeout := instancecontroller.InstanceReadyTimeout
			dbTimeout := instancecontroller.DatabaseInstanceReadyTimeoutSeeded
			if !isImageSeeded {
				dbTimeout = instancecontroller.DatabaseInstanceReadyTimeoutUnseeded
			}
			dbTimeout += 5 * time.Minute // Add some buffer time given that this test runs in a different process space than the instance

			By("By creating a new Instance")
			createInstance(instanceName, cdbName, instanceNamespace, version, edition, extra)
			instKey := client.ObjectKey{Namespace: instanceNamespace, Name: instanceName}

			By("By checking that Instance is created")
			testhelpers.WaitForInstanceConditionState(k8sEnv, instKey, k8s.Ready, metav1.ConditionTrue, k8s.CreateComplete, instanceTimeout)

			By("By checking that Database is provisioned")
			testhelpers.WaitForInstanceConditionState(k8sEnv, instKey, k8s.DatabaseInstanceReady, metav1.ConditionTrue, k8s.CreateComplete, dbTimeout)

			By("By checking that statefulset/deployment/svc are created")
			var sts appsv1.StatefulSetList
			Expect(k8sClient.List(ctx, &sts, client.InNamespace(instanceNamespace))).Should(Succeed())
			Expect(len(sts.Items)).Should(Equal(1))

			var instanceDeployment appsv1.DeploymentList
			Expect(k8sClient.List(ctx, &instanceDeployment, client.InNamespace(instanceNamespace))).Should(Succeed())
			Expect(len(instanceDeployment.Items)).Should(Equal(1)) //1 deployment for the instance

			var instanceSvc corev1.ServiceList
			Expect(k8sClient.List(ctx, &instanceSvc, client.InNamespace(instanceNamespace))).Should(Succeed())
			Expect(len(instanceSvc.Items)).Should(Equal(3)) // 3 services (LB, DBDaemon, Agent) per (1) instance
		})
	}

	// Images built using El Carro scripts
	Context("Oracle 19.3 EE", func() {
		TestInstanceCreationAndDatabaseProvisioning("19.3", "EE", "", true)
	})

	Context("Oracle 18c XE", func() {
		TestInstanceCreationAndDatabaseProvisioning("18c", "XE", "", true)
	})

	// Slow tests, only run in Canary
	if testhelpers.IsCanaryJob() {
		Context("Oracle 19.3 EE unseeded", func() {
			TestInstanceCreationAndDatabaseProvisioning("19.3", "EE", "32545013-unseeded", false)
		})

		// Images from OCR
		Context("Oracle 19.3 EE unseeded from OCR", func() {
			TestInstanceCreationAndDatabaseProvisioning("19.3", "EE", "ocr", false)
		})
	}
})

func createInstance(instanceName, cdbName, namespace, version, edition, extra string) {
	instance := &v1alpha1.Instance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      instanceName,
			Namespace: namespace,
		},
		Spec: v1alpha1.InstanceSpec{
			// Keep the CDBName in the spec different from the CDB name in the image (GCLOUD).
			// Doing this implicitly test the CDB renaming feature.
			CDBName:      cdbName,
			DBUniqueName: cdbName,
			InstanceSpec: commonv1alpha1.InstanceSpec{
				Version: version,
				Disks: []commonv1alpha1.DiskSpec{
					{
						Name: "DataDisk",
						Size: resource.MustParse("45Gi"),
					},
					{
						Name: "LogDisk",
						Size: resource.MustParse("55Gi"),
					},
				},
				DatabaseResources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceMemory: resource.MustParse("7Gi"),
					},
				},
				Images: map[string]string{
					"service": testhelpers.TestImageForVersion(version, edition, extra),
				},
			},
		},
	}
	testhelpers.K8sCreateWithRetry(k8sEnv.K8sClient, k8sEnv.Ctx, instance)
}

func TestInstance(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecsWithDefaultAndCustomReporters(t,
		t.Name(),
		[]Reporter{printer.NewlineReporter{}})
}
