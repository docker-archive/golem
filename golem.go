package main

import (
	"encoding/json"
	"flag"
	"io/ioutil"
	"os"
	"path/filepath"

	"golang.org/x/net/context"

	"github.com/Sirupsen/logrus"
	"github.com/docker/golem/runner"
	"github.com/docker/golem/versionutil"
)

func main() {
	name := filepath.Base(os.Args[0])
	if name == "golem_runner" {
		runnerMain()
		return
	}
	var (
		cacheDir    string
		startDaemon bool
		debug       bool
	)

	cm := runner.NewConfigurationManager(name)

	cm.FlagSet.StringVar(&cacheDir, "cache", "", "Cache directory")
	cm.FlagSet.BoolVar(&startDaemon, "rundaemon", false, "Start daemon")
	cm.FlagSet.BoolVar(&debug, "debug", false, "Whether to output debug logs")

	if err := cm.ParseFlags(os.Args[1:]); err != nil {
		logrus.Fatalf("Invalid options: %v", err)
	}

	if debug {
		logrus.SetLevel(logrus.DebugLevel)
	}

	runConfig, err := cm.RunnerConfiguration()
	if err != nil {
		logrus.Fatalf("Error creating run configuration: %v", err)
	}

	if cacheDir == "" {
		td, err := ioutil.TempDir("", "golem-cache-")
		if err != nil {
			logrus.Fatalf("Error creating tempdir: %v", err)
		}
		cacheDir = td
		defer os.RemoveAll(td)
	}

	cacheConfig := runner.CacheConfiguration{
		ImageCache: runner.NewImageCache(filepath.Join(cacheDir, "images")),
	}

	var client runner.DockerClient
	if startDaemon {
		logger := runner.NewConsoleLogCapturer()
		c, shutdown, err := runner.StartDaemon(context.Background(), "docker", logger)
		if err != nil {
			logrus.Fatalf("Error starting deamon: %v", err)
		}
		defer shutdown()
		client = c
	} else {
		c, err := cm.DockerClient()
		if err != nil {
			logrus.Fatalf("Failed to create client: %v", err)
		}
		client = c
	}

	// require running on docker 1.10 to ensure content addressable
	// image identifiers are used
	if err := client.CheckServerVersion(versionutil.StaticVersion(1, 10, 0)); err != nil {
		logrus.Fatal(err)
	}

	r := runner.NewRunner(runConfig, cacheConfig, debug)

	if err := r.Build(client); err != nil {
		logrus.Fatalf("Error building test images: %v", err)
	}

	if err := r.Run(client); err != nil {
		logrus.Fatalf("Error running tests: %v", err)
	}
}

func runnerMain() {
	var (
		command        string
		forwardAddress string
		dind           bool
		clean          bool
		debug          bool
	)

	flag.StringVar(&command, "command", "bats", "Command to run")
	flag.StringVar(&forwardAddress, "forward", "", "Address to forward logs to")
	flag.BoolVar(&dind, "docker", false, "Whether to run docker")
	flag.BoolVar(&clean, "clean", false, "Whether to ensure /var/lib/docker is empty")
	flag.BoolVar(&debug, "debug", false, "Whether to output debug logs")

	flag.Parse()

	if debug {
		logrus.SetLevel(logrus.DebugLevel)
	}

	router := runner.NewLogRouter("/var/log/docker")

	if forwardAddress != "" {
		logrus.Debugf("Forwarding logs to %s, not yet supported", forwardAddress)
		// TODO: Create forwarder with address
		// add forwarder using router.AddForwarder
	}

	logrus.Debugf("Runner!")

	logrus.Debugf("Environment: %#v", os.Environ())

	// Check if has compose file
	composeFile := "/runner/docker-compose.yml"
	var composeCapturer runner.LogCapturer
	if _, err := os.Stat(composeFile); err == nil {
		composeCapturer, err = router.RouteLogCapturer("compose")
		if err != nil {
			logrus.Fatalf("Error creating log capturer: %v", err)
		}
		defer composeCapturer.Close()
	} else {
		logrus.Debugf("No compose file found at %s", composeFile)
	}

	scriptCapturer, err := router.RouteLogCapturer("scripts")
	if err != nil {
		logrus.Fatalf("Error creating log capturer: %v", err)
	}
	defer scriptCapturer.Close()
	loadCapturer, err := router.RouteLogCapturer("load")
	if err != nil {
		logrus.Fatalf("Error creating log capturer: %v", err)
	}
	defer loadCapturer.Close()
	daemonCapturer, err := router.RouteLogCapturer("daemon")
	if err != nil {
		logrus.Fatalf("Error creating log capturer: %v", err)
	}
	defer daemonCapturer.Close()
	testCapturer, err := router.RouteLogCapturer("test")
	if err != nil {
		logrus.Fatalf("Error creating log capturer: %v", err)
	}
	defer testCapturer.Close()

	if forwardAddress == "" {
		logrus.Debugf("Logs not forwarded, dumping test output to console")
		if err := router.AddCapturer("test", runner.NewConsoleLogCapturer()); err != nil {
			logrus.Fatalf("Error creating test capturer")
		}
	}

	instanceF, err := os.Open("/instance.json")
	if err != nil {
		logrus.Fatalf("Error opening instance file: %v", err)
	}

	var instanceConfig runner.RunConfiguration
	if err := json.NewDecoder(instanceF).Decode(&instanceConfig); err != nil {
		logrus.Fatalf("Error decoding instance configuration: %v", err)
	}

	suiteConfig := runner.SuiteRunnerConfiguration{
		DockerLoadLogCapturer: loadCapturer,
		DockerLogCapturer:     daemonCapturer,

		RunConfiguration: instanceConfig,
		SetupLogCapturer: scriptCapturer,
		TestCapturer:     testCapturer,

		CleanDockerGraph: clean,
		DockerInDocker:   dind,
	}

	if composeCapturer != nil {
		suiteConfig.ComposeCapturer = composeCapturer
		suiteConfig.ComposeFile = composeFile

	}

	r := runner.NewSuiteRunner(suiteConfig)

	if err := r.Setup(); err != nil {
		logrus.Fatalf("Setup error: %v", err)
	}

	runErr := r.RunTests()

	if err := r.TearDown(); err != nil {
		logrus.Errorf("TearDown error: %v", err)
	}

	if runErr != nil {
		logrus.Fatalf("Test errored: %v", runErr)
	}

	logrus.Debugf("Shutting down log router")
	router.Shutdown()
}
