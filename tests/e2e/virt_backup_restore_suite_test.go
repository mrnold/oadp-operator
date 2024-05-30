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

	diskName := brCase.Name + "-disk"
	vmName := brCase.Name + "-vm"

	// Create DPA
	backupName, restoreName := prepareBackupAndRestore(brCase.BackupRestoreCase, func() {})

	err := lib.CreateNamespace(v.Clientset, brCase.Namespace)
	Expect(err).To(BeNil())

	// Create a standalone VM disk. This defaults to cloning the CirrOS image.
	// This uses a DataVolume so import and clone progress can be monitored.
	// Behavioral notes: CDI will garbage collect DataVolumes if they are not
	// attached to anything, so this disk needs to include the annotation
	// storage.deleteAfterCompletion in order to keep it around long enough to
	// attach to a VM. CDI also waits until the DataVolume is attached to
	// something before running whatever import process it has been asked to
	// run, so this disk also needs the storage.bind.immediate.requested
	// annotation to get it to start the cloning process.
	err = v.CreateDiskFromYaml(brCase.Namespace, diskName, 5*time.Minute)
	Expect(err).To(BeNil())

	err = v.CreateVm(brCase.Namespace, vmName, 5*time.Minute)
	Expect(err).To(BeNil())

	// Remove the Data Volume, but keep the PVC attached to the VM. The
	// DataVolume must be removed before backing up, because otherwise the
	// restore process might try to follow the instructions in the spec. For
	// example, a DataVolume that lists a cloned PVC will get restored in the
	// same state, and attempt to clone the PVC again after restore. The
	// DataVolume also needs to be detached from the VM, so that the VM restore
	// does not try to use the DataVolume that was deleted.
	err = v.DetachPvc(brCase.Namespace, diskName, 2*time.Minute)
	Expect(err).To(BeNil())
	err = v.RemoveDataVolume(brCase.Namespace, diskName, 2*time.Minute)
	Expect(err).To(BeNil())

	// Back up VM
	nsRequiresResticDCWorkaround := runBackup(brCase.BackupRestoreCase, backupName)

	// Delete everything in test namespace
	err = v.RemoveVm(brCase.Namespace, vmName, 2*time.Minute)
	Expect(err).To(BeNil())
	err = v.RemovePvc(brCase.Namespace, diskName, 2*time.Minute)
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
		Expect(err).To(BeNil())
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
				Namespace:         "cirros-test",
				Name:              "cirros-test",
				SkipVerifyLogs:    true,
				BackupRestoreType: lib.CSIDataMover,
				ReadyDelay:        1 * time.Minute,
				SnapshotVolumes:   true,
				RestorePVs:        true,
			},
		}, nil),
	)
})
