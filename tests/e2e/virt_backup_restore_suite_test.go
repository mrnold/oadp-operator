package e2e_test

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	. "github.com/onsi/gomega"

	"github.com/openshift/oadp-operator/api/v1alpha1"
	"github.com/openshift/oadp-operator/tests/e2e/lib"
)

func getLatestCirrosImageURL() (string, error) {
	cirrosVersionURL := "https://download.cirros-cloud.net/version/released"

	resp, err := http.Get(cirrosVersionURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	latestCirrosVersion := strings.TrimSpace(string(body))

	imageURL := fmt.Sprintf("https://download.cirros-cloud.net/%s/cirros-%s-x86_64-disk.img", latestCirrosVersion, latestCirrosVersion)

	return imageURL, nil
}

type VmBackupRestoreCase struct {
	BackupRestoreCase
	Source          string
	SourceNamespace string
}

func runVmBackupAndRestore(brCase VmBackupRestoreCase, expectedErr error, updateLastBRcase func(brCase VmBackupRestoreCase), updateLastInstallTime func(), v *lib.VirtOperator) {
	updateLastBRcase(brCase)

	// Create DPA
	backupName, restoreName := prepareBackupAndRestore(brCase.BackupRestoreCase, func() {})

	err := lib.CreateNamespace(v.Clientset, brCase.Namespace)
	Expect(err).To(BeNil())

	// Create VM from clone of CirrOS image
	err = v.CloneDisk(brCase.SourceNamespace, brCase.Source, brCase.Namespace, brCase.Name, 5*time.Minute)
	Expect(err).To(BeNil())

	err = v.CreateVm(brCase.Namespace, brCase.Name, brCase.Source, 5*time.Minute)
	Expect(err).To(BeNil())

	// Remove the Data Volume, but keep the PVC attached to the VM
	err = v.DetachPvc(brCase.Namespace, brCase.Name, 2*time.Minute)
	Expect(err).To(BeNil())
	err = v.RemoveDataVolume(brCase.Namespace, brCase.Name, 2*time.Minute)
	Expect(err).To(BeNil())

	// Back up VM
	nsRequiresResticDCWorkaround := runBackup(brCase.BackupRestoreCase, backupName)

	// Delete everything in test namespace
	err = v.RemoveVm(brCase.Namespace, brCase.Name, 2*time.Minute)
	Expect(err).To(BeNil())
	err = v.RemovePvc(brCase.Namespace, brCase.Name, 2*time.Minute)
	Expect(err).To(BeNil())
	err = lib.DeleteNamespace(v.Clientset, brCase.Namespace)
	Expect(err).To(BeNil())
	Eventually(lib.IsNamespaceDeleted(kubernetesClientForSuiteRun, brCase.Namespace), timeoutMultiplier*time.Minute*4, time.Second*5).Should(BeTrue())

	// Do restore
	runRestore(brCase.BackupRestoreCase, backupName, restoreName, nsRequiresResticDCWorkaround)
}

var _ = Describe("VM backup and restore tests", Ordered, func() {
	var v *lib.VirtOperator
	var err error
	wasInstalledFromTest := false
	var lastBRCase VmBackupRestoreCase
	var lastInstallTime time.Time
	updateLastBRcase := func(brCase VmBackupRestoreCase) {
		lastBRCase = brCase
	}
	updateLastInstallTime := func() {
		lastInstallTime = time.Now()
	}

	var _ = BeforeAll(func() {
		v, err = lib.GetVirtOperator(runTimeClientForSuiteRun, kubernetesClientForSuiteRun, dynamicClientForSuiteRun)
		gomega.Expect(err).To(BeNil())
		Expect(v).ToNot(BeNil())

		if !v.IsVirtInstalled() {
			err = v.EnsureVirtInstallation()
			Expect(err).To(BeNil())
			wasInstalledFromTest = true
		}

		err = v.EnsureEmulation(20 * time.Second)
		Expect(err).To(BeNil())

		url, err := getLatestCirrosImageURL()
		Expect(err).To(BeNil())
		err = v.EnsureDataVolumeFromUrl("openshift-cnv", "cirros-dv", url, "128Mi", 5*time.Minute)
		Expect(err).To(BeNil())

		dpaCR.CustomResource.Spec.Configuration.Velero.DefaultPlugins = append(dpaCR.CustomResource.Spec.Configuration.Velero.DefaultPlugins, v1alpha1.DefaultPluginKubeVirt)
	})

	var _ = AfterAll(func() {
		v.RemoveDataVolume("openshift-cnv", "cirros-dv", 2*time.Minute)

		if v != nil && wasInstalledFromTest {
			v.EnsureVirtRemoval()
		}
	})

	var _ = AfterEach(func(ctx SpecContext) {
		tearDownBackupAndRestore(lastBRCase.BackupRestoreCase, lastInstallTime, ctx.SpecReport())
	})

	DescribeTable("Backup and restore virtual machines",
		func(brCase VmBackupRestoreCase, expectedError error) {
			runVmBackupAndRestore(brCase, expectedError, updateLastBRcase, updateLastInstallTime, v)
		},

		Entry("default virtual machine backup and restore", Label("virt"), VmBackupRestoreCase{
			Source:          "cirros-dv",
			SourceNamespace: "openshift-cnv",
			BackupRestoreCase: BackupRestoreCase{
				Namespace:         "cirros-test-vm",
				Name:              "cirros-vm",
				SkipVerifyLogs:    true,
				BackupRestoreType: lib.CSIDataMover,
				ReadyDelay:        1 * time.Minute,
				SnapshotVolumes:   true,
				RestorePVs:        true,
			},
		}, nil),
	)
})
