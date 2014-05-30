package integration_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/cloudfoundry/gunk/natsrunner"
	"github.com/cloudfoundry/gunk/timeprovider"
	"github.com/cloudfoundry/yagnats"

	Bbs "github.com/cloudfoundry-incubator/runtime-schema/bbs"
	"github.com/cloudfoundry-incubator/runtime-schema/bbs/services_bbs"
	"github.com/cloudfoundry-incubator/stager/integration/stager_runner"
	steno "github.com/cloudfoundry/gosteno"
	"github.com/cloudfoundry/storeadapter/storerunner/etcdstorerunner"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gexec"
)

var stagerPath string
var etcdRunner *etcdstorerunner.ETCDClusterRunner
var natsRunner *natsrunner.NATSRunner
var runner *stager_runner.StagerRunner

var _ = Describe("Main", func() {
	var (
		natsClient         yagnats.NATSClient
		bbs                *Bbs.BBS
		fileServerPresence services_bbs.Presence
		presenceStatus     <-chan bool
	)

	BeforeEach(func() {
		etcdPort := 5001 + GinkgoParallelNode()
		natsPort := 4001 + GinkgoParallelNode()

		etcdRunner = etcdstorerunner.NewETCDClusterRunner(etcdPort, 1)
		etcdRunner.Start()

		natsRunner = natsrunner.NewNATSRunner(natsPort)
		natsRunner.Start()

		natsClient = natsRunner.MessageBus

		logSink := steno.NewTestingSink()

		steno.Init(&steno.Config{
			Sinks: []steno.Sink{logSink},
		})

		logger := steno.NewLogger("the-logger")
		steno.EnterTestMode()

		bbs = Bbs.NewBBS(etcdRunner.Adapter(), timeprovider.NewTimeProvider(), logger)

		var err error

		fileServerPresence, presenceStatus, err = bbs.MaintainFileServerPresence(time.Second, "http://example.com", "file-server-id")
		Ω(err).ShouldNot(HaveOccurred())
		Eventually(presenceStatus).Should(Receive(BeTrue()))

		runner = stager_runner.New(
			stagerPath,
			[]string{fmt.Sprintf("http://127.0.0.1:%d", etcdPort)},
			[]string{fmt.Sprintf("127.0.0.1:%d", natsPort)},
		)
	})

	AfterEach(func(done Done) {
		runner.Stop()
		go func() {
			<-presenceStatus
		}()
		fileServerPresence.Remove()
		etcdRunner.Stop()
		natsRunner.Stop()
		close(done)
	}, 10.0)

	Context("when started", func() {
		BeforeEach(func() {
			runner.Start("--circuses", `{"lucid64":"lifecycle.zip"}`)
		})

		Describe("when a 'diego.staging.start' message is recieved", func() {
			BeforeEach(func() {
				natsClient.Publish("diego.staging.start", []byte(`
				      {
				        "app_id":"my-app-guid",
                "task_id":"my-task-guid",
                "stack":"lucid64",
                "app_bits_download_uri":"http://example.com/app_bits",
                "file_descriptors":3,
                "memory_mb" : 1024,
                "disk_mb" : 128,
                "buildpacks" : [],
                "environment" : []
				      }
				    `))
			})

			It("desires a staging task via the BBS", func() {
				Eventually(bbs.GetAllPendingTasks, 1.0).Should(HaveLen(1))
			})

			It("does not exit", func() {
				Consistently(runner.Session()).ShouldNot(gexec.Exit())
			})
		})
	})
})

func TestStagerMain(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Integration Suite")
}

var _ = BeforeSuite(func() {
	var err error
	stagerPath, err = gexec.Build("github.com/cloudfoundry-incubator/stager", "-race")
	Ω(err).ShouldNot(HaveOccurred())
})

var _ = AfterSuite(func() {
	gexec.CleanupBuildArtifacts()
	if etcdRunner != nil {
		etcdRunner.Stop()
	}
	if natsRunner != nil {
		natsRunner.Stop()
	}
	if runner != nil {
		runner.Stop()
	}
})
