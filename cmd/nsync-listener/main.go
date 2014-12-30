package main

import (
	"encoding/json"
	"errors"
	"flag"
	"os"
	"strings"
	"time"

	"github.com/cloudfoundry-incubator/cf-debug-server"
	"github.com/cloudfoundry-incubator/cf-http"
	"github.com/cloudfoundry-incubator/cf-lager"
	"github.com/cloudfoundry-incubator/receptor"
	Bbs "github.com/cloudfoundry-incubator/runtime-schema/bbs"
	"github.com/cloudfoundry-incubator/runtime-schema/bbs/lock_bbs"
	"github.com/cloudfoundry/gunk/diegonats"
	"github.com/cloudfoundry/gunk/timeprovider"
	"github.com/cloudfoundry/gunk/workpool"
	"github.com/cloudfoundry/storeadapter/etcdstoreadapter"
	"github.com/nu7hatch/gouuid"
	"github.com/pivotal-golang/lager"
	"github.com/tedsuo/ifrit"
	"github.com/tedsuo/ifrit/grouper"
	"github.com/tedsuo/ifrit/sigmon"

	"github.com/cloudfoundry-incubator/nsync/listen"
	"github.com/cloudfoundry-incubator/nsync/recipebuilder"
	"github.com/cloudfoundry/dropsonde"
)

var heartbeatInterval = flag.Duration(
	"heartbeatInterval",
	lock_bbs.HEARTBEAT_INTERVAL,
	"the interval between heartbeats to the lock",
)

var etcdCluster = flag.String(
	"etcdCluster",
	"http://127.0.0.1:4001",
	"comma-separated list of etcd addresses (http://ip:port)",
)

var diegoAPIURL = flag.String(
	"diegoAPIURL",
	"",
	"URL of diego API",
)

var natsAddresses = flag.String(
	"natsAddresses",
	"127.0.0.1:4222",
	"comma-separated list of NATS addresses (ip:port)",
)

var natsUsername = flag.String(
	"natsUsername",
	"nats",
	"Username to connect to nats",
)

var natsPassword = flag.String(
	"natsPassword",
	"nats",
	"Password for nats user",
)

var circuses = flag.String(
	"circuses",
	"",
	"app lifecycle binary bundle mapping (stack => bundle filename in fileserver)",
)

var dockerCircusPath = flag.String(
	"dockerCircusPath",
	"",
	"path for downloading docker circus from file server",
)

var fileServerURL = flag.String(
	"fileServerURL",
	"",
	"URL of the file server",
)

var dropsondeOrigin = flag.String(
	"dropsondeOrigin",
	"nsync_listener",
	"Origin identifier for dropsonde-emitted metrics.",
)

var dropsondeDestination = flag.String(
	"dropsondeDestination",
	"localhost:3457",
	"Destination for dropsonde-emitted metrics.",
)

var communicationTimeout = flag.Duration(
	"communicationTimeout",
	30*time.Second,
	"Timeout applied to all HTTP requests.",
)

func init() {
	flag.Parse()
	cf_http.Initialize(*communicationTimeout)
}

func main() {
	logger := cf_lager.New("nsync-listener")

	initializeDropsonde(logger)

	diegoAPIClient := receptor.NewClient(*diegoAPIURL)
	bbs := initializeBbs(logger)

	cf_debug_server.Run()

	var circuseDownloadURLs map[string]string
	err := json.Unmarshal([]byte(*circuses), &circuseDownloadURLs)

	if *dockerCircusPath == "" {
		logger.Fatal("empty-docker-circus-path", errors.New("dockerCircusPath flag not provided"))
	}

	if err != nil {
		logger.Fatal("invalid-circus-mapping", err)
	}

	recipeBuilder := recipebuilder.New(circuseDownloadURLs, *dockerCircusPath, *fileServerURL, logger)

	uuid, err := uuid.NewV4()
	if err != nil {
		logger.Fatal("Couldn't generate uuid", err)
	}

	nsyncLock := bbs.NewNsyncListenerLock(uuid.String(), *heartbeatInterval)
	natsClient := diegonats.NewClient()
	natsClientRunner := diegonats.NewClientRunner(*natsAddresses, *natsUsername, *natsPassword, logger, natsClient)
	listener := listen.Listen{
		NATSClient:     natsClient,
		ReceptorClient: diegoAPIClient,
		Logger:         logger,
		RecipeBuilder:  recipeBuilder,
	}

	group := grouper.NewOrdered(os.Interrupt, grouper.Members{
		{"nsyncLock", nsyncLock},
		{"nats-client", natsClientRunner},
		{"listener", listener},
	})

	logger.Info("waiting-for-lock")

	monitor := ifrit.Envoke(sigmon.New(group))

	logger.Info("started")

	err = <-monitor.Wait()
	if err != nil {
		logger.Error("exited-with-failure", err)
		os.Exit(1)
	}

	logger.Info("exited")
	os.Exit(0)
}

func initializeDropsonde(logger lager.Logger) {
	err := dropsonde.Initialize(*dropsondeDestination, *dropsondeOrigin)
	if err != nil {
		logger.Error("failed to initialize dropsonde: %v", err)
	}
}

func initializeBbs(logger lager.Logger) Bbs.NsyncBBS {
	etcdAdapter := etcdstoreadapter.NewETCDStoreAdapter(
		strings.Split(*etcdCluster, ","),
		workpool.NewWorkPool(10),
	)

	err := etcdAdapter.Connect()
	if err != nil {
		logger.Fatal("failed-to-connect-to-etcd", err)
	}

	return Bbs.NewNsyncBBS(etcdAdapter, timeprovider.NewTimeProvider(), logger)
}
