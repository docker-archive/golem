package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Sirupsen/logrus"
	"github.com/dmcgowan/golem/versionutil"
	"github.com/docker/distribution/reference"
	dockerclient "github.com/fsouza/go-dockerclient"
)

type SuiteRunnerConfiguration struct {
	DockerInDocker        bool
	CleanDockerGraph      bool
	DockerLoadLogCapturer LogCapturer
	DockerLogCapturer     LogCapturer

	SetupScripts     [][]string
	SetupLogCapturer LogCapturer

	ComposeFile     string
	ComposeCapturer LogCapturer

	TestCommand  string
	TestArgs     []string
	TestEnv      []string
	TestCapturer LogCapturer
}

type SuiteRunner struct {
	config SuiteRunnerConfiguration

	daemonCloser func() error
}

func NewSuiteRunner(config SuiteRunnerConfiguration) *SuiteRunner {
	return &SuiteRunner{
		config: config,
	}
}

func (sr *SuiteRunner) Setup() error {
	// Setup /var/lib/docker
	if sr.config.DockerInDocker {
		// Check if empty
		info, err := ioutil.ReadDir("/var/lib/docker")
		if err != nil {
			return fmt.Errorf("error reading /var/lib/docker: %v", err)
		}

		if len(info) != 0 {
			// TODO: Clean if configuration is set
			logrus.Debugf("/var/lib/docker is not clean")
		}

		// Load tag map
		logrus.Debugf("Loading docker images")
		pc, pk, err := StartDaemon("/usr/bin/docker-load", sr.config.DockerLoadLogCapturer)
		if err != nil {
			return fmt.Errorf("error starting daemon: %v", err)
		}

		// Remove all containers
		containers, err := pc.ListContainers(dockerclient.ListContainersOptions{All: true})
		if err != nil {
			return fmt.Errorf("error listing containers: %v", err)
		}
		for _, container := range containers {
			logrus.Debugf("Removing container %s", container.ID)
			removeOptions := dockerclient.RemoveContainerOptions{
				ID:            container.ID,
				RemoveVolumes: true,
				Force:         true,
			}
			if err := pc.RemoveContainer(removeOptions); err != nil {
				return fmt.Errorf("error removing container: %v", err)
			}
		}

		if err := syncImages(pc, "/images"); err != nil {
			return fmt.Errorf("error syncing images: %v", err)
		}

		logrus.Debugf("Stopping daemon")
		if err := pk(); err != nil {
			return fmt.Errorf("error killing daemon %v", err)
		}

	}

	// Run all setup scripts
	for _, setupScript := range sr.config.SetupScripts {
		if err := RunScript(sr.config.SetupLogCapturer, setupScript[0], setupScript[1:]...); err != nil {
			return fmt.Errorf("error running setup script %s: %s", setupScript[0], err)
		}
	}

	// Start Docker-in-Docker daemon for tests, build compose images
	if sr.config.DockerInDocker {
		logrus.Debugf("Starting daemon")
		_, k, err := StartDaemon("/usr/bin/docker", sr.config.DockerLogCapturer)
		if err != nil {
			return fmt.Errorf("error starting daemon: %s", err)
		}
		sr.daemonCloser = k

		if sr.config.ComposeFile != "" {
			logrus.Debugf("Build compose images")
			if err := RunScript(sr.config.ComposeCapturer, "docker-compose", "-f", sr.config.ComposeFile, "build", "--no-cache"); err != nil {
				return fmt.Errorf("error running docker compose build: %v", err)
			}

			if err := RunScript(sr.config.ComposeCapturer, "docker-compose", "-f", sr.config.ComposeFile, "up", "-d"); err != nil {
				return fmt.Errorf("error running docker compose up: %v", err)
			}

			go func() {
				logrus.Debugf("Listening for logs")
				if err := RunScript(sr.config.ComposeCapturer, "docker-compose", "-f", sr.config.ComposeFile, "logs"); err != nil {
					logrus.Errorf("Error running docker compose logs: %v", err)
				}
			}()
		}
	}

	return nil
}

func (sr *SuiteRunner) TearDown() (err error) {
	if sr.config.DockerInDocker {
		if sr.config.ComposeFile != "" {
			if err := RunScript(sr.config.ComposeCapturer, "docker-compose", "-f", sr.config.ComposeFile, "stop"); err != nil {
				logrus.Errorf("Error stopping docker compose: %v", err)
			}
		}

		if err = sr.daemonCloser(); err != nil {
			logrus.Errorf("Error stopping daemon: %v", err)
		}
	}

	return
}

func (sr *SuiteRunner) RunTests() error {
	cmd := exec.Command(sr.config.TestCommand, sr.config.TestArgs...)
	// TODO: Parse Stdout
	cmd.Stdout = sr.config.TestCapturer.Stdout()
	cmd.Stderr = sr.config.TestCapturer.Stderr()
	cmd.Env = sr.config.TestEnv
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("could not start test command: %s", err)
	}
	return cmd.Wait()
}

// RunScript rungs the script command attaching
// results to stdout and stdout
func RunScript(lc LogCapturer, script string, args ...string) error {
	cmd := exec.Command(script, args...)
	cmd.Stdout = lc.Stdout()
	cmd.Stderr = lc.Stderr()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("could not start script: %s", err)
	}
	return cmd.Wait()
}

// StartDaemon starts a daemon using the provided binary returning
// a client to the binary, a close function, and error.
func StartDaemon(binary string, lc LogCapturer) (*dockerclient.Client, func() error, error) {
	// Get Docker version of process
	previousVersion, err := versionutil.BinaryVersion(binary)
	if err != nil {
		return nil, nil, fmt.Errorf("could not get binary version: %s", err)
	}

	logrus.Debugf("Starting daemon with %s", binary)
	binaryArgs := []string{}
	if previousVersion.LessThan(versionutil.StaticVersion(1, 8, 0)) {
		binaryArgs = append(binaryArgs, "--daemon")
	} else {
		binaryArgs = append(binaryArgs, "daemon")
	}
	binaryArgs = append(binaryArgs, "--log-level=debug")
	binaryArgs = append(binaryArgs, "--storage-driver="+getGraphDriver())
	cmd := exec.Command(binary, binaryArgs...)
	cmd.Stdout = lc.Stdout()
	cmd.Stderr = lc.Stderr()
	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("could not start daemon: %s", err)
	}

	logrus.Debugf("Waiting for daemon to start")
	time.Sleep(2 * time.Second)

	client, err := dockerclient.NewClientFromEnv()
	if err != nil {
		return nil, nil, fmt.Errorf("could not initialize client: %s", err)
	}

	// Wait for it to start
	for i := 0; ; i++ {
		v, err := client.Version()
		if err == nil {
			logrus.Debugf("Established connection to daemon with version %s", v.Get("Version"))
			break
		}
		if i >= 10 {
			logrus.Fatalf("Failed to establish connection to daemon, check logs, quitting")
		}
		time.Sleep(time.Second)
	}

	return client, cmd.Process.Kill, nil
}

type tagMap map[string][]string

func listDiff(l1, l2 []string) ([]string, []string) {
	sort.Strings(l1)
	sort.Strings(l2)

	removed := []string{}
	added := []string{}

	i1 := 0
	i2 := 0
	for i1 < len(l1) && i2 < len(l2) {
		r := strings.Compare(l1[i1], l2[i2])
		switch {
		case r < 0:
			removed = append(removed, l1[i1])
			i1++
		case r > 0:
			added = append(added, l2[i2])
			i2++
		default:
			i1++
			i2++
		}
	}
	for i1 < len(l1) {
		removed = append(removed, l1[i1])
		i1++
	}
	for i2 < len(l2) {
		added = append(added, l2[i2])
		i2++
	}

	return removed, added
}

func syncImages(client *dockerclient.Client, imageRoot string) error {
	logrus.Debugf("Syncing images from %s", imageRoot)
	f, err := os.Open(filepath.Join(imageRoot, "images.json"))
	if err != nil {
		return fmt.Errorf("error opening image json file: %v", err)
	}
	defer f.Close()

	var m tagMap
	if err := json.NewDecoder(f).Decode(&m); err != nil {
		return fmt.Errorf("error decoding images json: %v", err)
	}

	allTags := map[string]struct{}{}
	neededImages := map[string]struct{}{}
	for imageID, tags := range m {
		neededImages[imageID] = struct{}{}
		for _, t := range tags {
			allTags[t] = struct{}{}
		}
	}

	images, err := client.ListImages(dockerclient.ListImagesOptions{})
	if err != nil {
		return fmt.Errorf("error listing images: %v", err)
	}

	for _, img := range images {
		expectedTags, ok := m[img.ID]
		if ok {
			delete(neededImages, img.ID)

			repoTags := filterRepoTags(img.RepoTags)
			logrus.Debugf("Tags for %s: %#v", img.ID, repoTags)

			// Sync tags for image ID
			removedTags, addedTags := listDiff(repoTags, expectedTags)
			for _, t := range addedTags {
				if err := tagImage(client, img.ID, t); err != nil {
					return err
				}
			}
			for _, t := range removedTags {
				// Check if this image tag conflicts with an expected
				// tag, in which case force tag will update
				if _, ok := allTags[t]; !ok {
					logrus.Debugf("Removing tag %s", t)
					if err := client.RemoveImage(t); err != nil {
						return fmt.Errorf("error removing tag %s: %v", t, err)
					}
				}
			}
		} else {
			removeOptions := dockerclient.RemoveImageOptions{
				Force: true,
			}
			if err := client.RemoveImageExtended(img.ID, removeOptions); err != nil {
				return fmt.Errorf("error moving image %s: %v", img.ID, err)
			}
		}

	}

	for imageID := range neededImages {
		tags, ok := m[imageID]
		if !ok {
			return fmt.Errorf("missing image %s in tag map", imageID)
		}
		_, err := client.InspectImage(imageID)
		if err != nil {
			tf, err := os.Open(filepath.Join(imageRoot, imageID+".tar"))
			if err != nil {
				return fmt.Errorf("error opening image tar %s: %v", imageID, err)
			}
			defer tf.Close()
			loadOptions := dockerclient.LoadImageOptions{
				InputStream: tf,
			}
			if err := client.LoadImage(loadOptions); err != nil {
				return fmt.Errorf("error loading image %s: %v", imageID, err)
			}
		}
		for _, t := range tags {
			if err := tagImage(client, imageID, t); err != nil {
				return err
			}
		}
	}

	return nil
}

func filterRepoTags(tags []string) []string {
	filtered := make([]string, 0, len(tags))
	for _, tag := range tags {
		if tag != "<none>" && tag != "<none>:<none>" {
			filtered = append(filtered, tag)
		}
	}
	return filtered
}

func tagImage(client *dockerclient.Client, img, tag string) error {
	ref, err := reference.Parse(tag)
	if err != nil {
		return fmt.Errorf("invalid tag %s: %v", tag, err)
	}
	namedTagged, ok := ref.(reference.NamedTagged)
	if !ok {
		return fmt.Errorf("expecting named tagged reference: %s", tag)
	}
	tagOptions := dockerclient.TagImageOptions{
		Repo:  namedTagged.Name(),
		Tag:   namedTagged.Tag(),
		Force: true,
	}
	if err := client.TagImage(img, tagOptions); err != nil {
		return fmt.Errorf("error tagging image %s as %s: %v", img, tag, err)
	}

	return nil
}
