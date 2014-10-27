package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/cloudfoundry/gunk/diegonats"
	"github.com/cloudfoundry/gunk/timeprovider"
	"github.com/cloudfoundry/gunk/workpool"
	"github.com/cloudfoundry/storeadapter/etcdstoreadapter"
	"github.com/pivotal-golang/lager"
	"github.com/tedsuo/ifrit"
	"github.com/tedsuo/ifrit/grouper"
	"github.com/tedsuo/ifrit/sigmon"

	"github.com/cloudfoundry-incubator/cf-debug-server"
	"github.com/cloudfoundry-incubator/cf-lager"
	"github.com/cloudfoundry-incubator/receptor"
	"github.com/cloudfoundry-incubator/runtime-schema/bbs"
	"github.com/cloudfoundry-incubator/stager/cc_client"
	"github.com/cloudfoundry-incubator/stager/inbox"
	"github.com/cloudfoundry-incubator/stager/outbox"
	"github.com/cloudfoundry-incubator/stager/stager"
	"github.com/cloudfoundry-incubator/stager/stager_docker"
	_ "github.com/cloudfoundry/dropsonde/autowire"
)

var etcdCluster = flag.String(
	"etcdCluster",
	"",
	"comma-separated list of etcd addresses (http://ip:port)",
)

var natsAddresses = flag.String(
	"natsAddresses",
	"",
	"comma-separated list of NATS addresses (ip:port)",
)

var natsUsername = flag.String(
	"natsUsername",
	"",
	"Username to connect to nats",
)

var natsPassword = flag.String(
	"natsPassword",
	"",
	"Password for nats user",
)

var ccBaseURL = flag.String(
	"ccBaseURL",
	"",
	"URI to acccess the Cloud Controller",
)

var ccUsername = flag.String(
	"ccUsername",
	"",
	"Basic auth username for CC internal API",
)

var ccPassword = flag.String(
	"ccPassword",
	"",
	"Basic auth password for CC internal API",
)

var skipCertVerify = flag.Bool(
	"skipCertVerify",
	false,
	"skip SSL certificate verification",
)

var circuses = flag.String(
	"circuses",
	"{}",
	"Map of circuses for different stacks (name => compiler_name)",
)

var dockerCircusPath = flag.String(
	"dockerCircusPath",
	"",
	"path for downloading docker circus from file server",
)

var minMemoryMB = flag.Uint(
	"minMemoryMB",
	1024,
	"minimum memory limit for staging tasks",
)

var minDiskMB = flag.Uint(
	"minDiskMB",
	3072,
	"minimum disk limit for staging tasks",
)

var minFileDescriptors = flag.Uint64(
	"minFileDescriptors",
	0,
	"minimum file descriptors for staging tasks",
)

var diegoAPIURL = flag.String(
	"diegoAPIURL",
	"",
	"URL of diego API",
)

var listenAddr = flag.String(
	"listenAddr",
	"",
	"address on which to listen for staging task completion callbacks",
)

func main() {
	flag.Parse()

	logger := cf_lager.New("stager")
	stagerBBS := initializeStagerBBS(logger)
	traditionalStager, dockerStager := initializeStagers(stagerBBS, logger)
	ccClient := cc_client.NewCcClient(*ccBaseURL, *ccUsername, *ccPassword, *skipCertVerify)

	cf_debug_server.Run()

	natsClient := diegonats.NewClient()

	group := grouper.NewOrdered(os.Interrupt, grouper.Members{
		{"nats", diegonats.NewClientRunner(*natsAddresses, *natsUsername, *natsPassword, logger, natsClient)},
		{"inbox", ifrit.RunFunc(func(signals <-chan os.Signal, ready chan<- struct{}) error {
			return inbox.New(natsClient, ccClient, traditionalStager, dockerStager, inbox.ValidateRequest, logger).Run(signals, ready)
		})},
		{"outbox", outbox.New(*listenAddr, ccClient, logger, timeprovider.NewTimeProvider())},
	})

	process := ifrit.Envoke(sigmon.New(group))

	fmt.Println("Listening for staging requests!")

	err := <-process.Wait()
	if err != nil {
		logger.Fatal("Stager exited with error: %s", err)
	}
}

func initializeStagers(stagerBBS bbs.StagerBBS, logger lager.Logger) (stager.Stager, stager_docker.DockerStager) {
	circusesMap := make(map[string]string)
	err := json.Unmarshal([]byte(*circuses), &circusesMap)
	if err != nil {
		logger.Fatal("Error parsing circuses flag: %s\n", err)
	}
	config := stager.Config{
		Circuses:           circusesMap,
		DockerCircusPath:   *dockerCircusPath,
		MinMemoryMB:        *minMemoryMB,
		MinDiskMB:          *minDiskMB,
		MinFileDescriptors: *minFileDescriptors,
	}

	diegoAPIClient := receptor.NewClient(*diegoAPIURL, "", "")

	bpStager := stager.New(stagerBBS, diegoAPIClient, logger, config)
	dockerStager := stager_docker.New(stagerBBS, diegoAPIClient, logger, config)

	return bpStager, dockerStager
}

func initializeStagerBBS(logger lager.Logger) bbs.StagerBBS {
	etcdAdapter := etcdstoreadapter.NewETCDStoreAdapter(
		strings.Split(*etcdCluster, ","),
		workpool.NewWorkPool(10),
	)

	err := etcdAdapter.Connect()
	if err != nil {
		logger.Fatal("failed-to-connect-to-etcd", err)
	}

	return bbs.NewStagerBBS(etcdAdapter, timeprovider.NewTimeProvider(), logger)
}
