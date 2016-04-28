package main

import (
	"encoding/json"
	"flag"
	"io/ioutil"
	"os"
	"path/filepath"

	"golang.org/x/net/context"

	"github.com/Sirupsen/logrus"
	"github.com/docker/golem/clientutil"
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
	)

	co := clientutil.NewClientOptions()
	cm := runner.NewConfigurationManager()

	flag.StringVar(&cacheDir, "cache", "", "Cache directory")
	flag.BoolVar(&startDaemon, "rundaemon", false, "Start daemon")
	// TODO: Add swarm flag and host option

	flag.Parse()

	// TODO: Allow quiet and verbose mode
	logrus.SetLevel(logrus.DebugLevel)

	if cacheDir == "" {
		td, err := ioutil.TempDir("", "golem-cache-")
		if err != nil {
			logrus.Fatalf("Error creating tempdir: %v", err)
		}
		cacheDir = td
		defer os.RemoveAll(td)
	}

	c := runner.CacheConfiguration{
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
		c, err := runner.NewDockerClient(co)
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

	r, err := cm.CreateRunner(c)
	if err != nil {
		logrus.Fatalf("Error creating runner: %v", err)
	}

	if err := r.Build(client); err != nil {
		logrus.Fatalf("Error building test images: %v", err)
	}

	if err := r.Run(client); err != nil {
		logrus.Fatalf("Error running tests: %v", err)
	}
}

func runnerMain() {
	var (
		command string
		dind    bool
		clean   bool
	)

	// TODO: Parse runner options
	flag.StringVar(&command, "command", "bats", "Command to run")
	flag.BoolVar(&dind, "docker", false, "Whether to run docker")
	flag.BoolVar(&clean, "clean", false, "Whether to ensure /var/lib/docker is empty")

	flag.Parse()

	// TODO: Allow quiet and verbose mode
	logrus.SetLevel(logrus.DebugLevel)

	logrus.Debugf("Runner!")

	// Check if has compose file
	composeFile := "/runner/docker-compose.yml"
	var composeCapturer runner.LogCapturer
	if _, err := os.Stat(composeFile); err == nil {
		composeCapturer = newFileCapturer("compose")
		defer composeCapturer.Close()
	} else {
		logrus.Debugf("No compose file found at %s", composeFile)
	}
	logrus.Debugf("Environment: %#v", os.Environ())

	scriptCapturer := newFileCapturer("scripts")
	defer scriptCapturer.Close()
	loadCapturer := newFileCapturer("load")
	defer loadCapturer.Close()
	daemonCapturer := newFileCapturer("daemon")
	defer daemonCapturer.Close()
	testCapturer := runner.NewConsoleLogCapturer()
	defer testCapturer.Close()

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
}

func newFileCapturer(name string) runner.LogCapturer {
	basename := filepath.Join("/var/log/docker", name)
	lc, err := runner.NewFileLogCapturer(basename)
	if err != nil {
		logrus.Fatalf("Error creating file capturer for %s: %v", basename, err)
	}

	return lc
}
