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
	"github.com/docker/distribution/reference"
	"github.com/docker/golem/versionutil"
	dockerclient "github.com/fsouza/go-dockerclient"
)

type SuiteRunnerConfiguration struct {
	DockerInDocker        bool
	CleanDockerGraph      bool
	DockerLoadLogCapturer LogCapturer
	DockerLogCapturer     LogCapturer

	ComposeFile     string
	ComposeCapturer LogCapturer

	RunConfiguration RunConfiguration
	SetupLogCapturer LogCapturer
	TestCapturer     LogCapturer
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

			loadVersion, err := versionutil.BinaryVersion("/usr/bin/docker-load")
			if err != nil {
				return err
			}

			if err := cleanDockerGraph("/var/lib/docker", loadVersion); err != nil {
				return err
			}
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
	for _, setupScript := range sr.config.RunConfiguration.Setup {
		if err := RunScript(sr.config.SetupLogCapturer, setupScript); err != nil {
			return fmt.Errorf("error running setup script %s: %s", setupScript.Command[0], err)
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
			buildScript := Script{
				Command: []string{"docker-compose", "-f", sr.config.ComposeFile, "build", "--no-cache"},
			}
			if err := RunScript(sr.config.ComposeCapturer, buildScript); err != nil {
				return fmt.Errorf("error running docker compose build: %v", err)
			}
			upScript := Script{
				Command: []string{"docker-compose", "-f", sr.config.ComposeFile, "up", "-d"},
			}

			if err := RunScript(sr.config.ComposeCapturer, upScript); err != nil {
				return fmt.Errorf("error running docker compose up: %v", err)
			}

			go func() {
				logrus.Debugf("Listening for logs")
				logScript := Script{
					Command: []string{"docker-compose", "-f", sr.config.ComposeFile, "logs"},
				}
				if err := RunScript(sr.config.ComposeCapturer, logScript); err != nil {
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
			stopScript := Script{
				Command: []string{"docker-compose", "-f", sr.config.ComposeFile, "stop"},
			}
			if err := RunScript(sr.config.ComposeCapturer, stopScript); err != nil {
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
	for _, runner := range sr.config.RunConfiguration.TestRunner {
		cmd := exec.Command(runner.Command[0], runner.Command[1:]...)
		// TODO: Parse Stdout using sr.config.RunConfiguration.TestRunner.Format
		cmd.Stdout = sr.config.TestCapturer.Stdout()
		cmd.Stderr = sr.config.TestCapturer.Stderr()
		cmd.Env = runner.Env
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("run error: %s", err)
		}
	}

	return nil
}

// RunScript runs the script command attaching
// results to stdout and stdout
func RunScript(lc LogCapturer, script Script) error {
	cmd := exec.Command(script.Command[0], script.Command[1:]...)
	cmd.Stdout = lc.Stdout()
	cmd.Stderr = lc.Stderr()
	cmd.Env = script.Env
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

func removeIfExists(path string) error {
	_, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	logrus.Debugf("Removing %s", path)
	return os.Remove(path)
}

func cleanDockerGraph(graphDir string, v versionutil.Version) error {
	logrus.Debugf("Cleaning for version %s", v)
	// Handle migration files
	migratedVersion := versionutil.StaticVersion(1, 10, 0)
	migratedVersion.Tag = "dev"
	if v.LessThan(migratedVersion) {

		if err := removeIfExists(filepath.Join(graphDir, ".migration-v1-images.json")); err != nil {
			return err
		}
		if err := removeIfExists(filepath.Join(graphDir, ".migration-v1-tags")); err != nil {
			return err
		}

		root := filepath.Join(graphDir, "graph")
		migrationPurger := func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if !info.IsDir() {
				if strings.HasPrefix(filepath.Base(path), ".migrat") {
					logrus.Debugf("Removing migration file %s", path)
					if err := os.Remove(path); err != nil {
						return err
					}
				}
			}
			return nil
		}
		if err := filepath.Walk(root, migrationPurger); err != nil {
			return err
		}

		// Remove all containers
		infos, err := ioutil.ReadDir(filepath.Join(graphDir, "containers"))
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		drivers, err := getAllGraphDrivers(graphDir)
		if err != nil {
			return err
		}
		for _, info := range infos {
			container := info.Name()
			for _, graphDriver := range drivers {
				if err := removeLayerGraphContent(container, "mount-id", graphDriver, graphDir); err != nil && !os.IsNotExist(err) {
					return err
				}
				if err := removeLayerGraphContent(container, "init-id", graphDriver, graphDir); err != nil && !os.IsNotExist(err) {
					return err
				}
			}
			if err := os.RemoveAll(filepath.Join(graphDir, "containers", container)); err != nil {
				return err
			}
		}
		if err := os.RemoveAll(filepath.Join(graphDir, "image")); err != nil {
			return err
		}
		if err := removeIfExists(filepath.Join(graphDir, "linkgraph.db")); err != nil {
			return err
		}

		// TODO: Remove everything in graph driver directory which is not in graph
	}

	return nil
}

func removeLayerGraphContent(layerID, filename, graphDriver, root string) error {
	layerRoot := filepath.Join(root, "image", graphDriver, "layerdb", "mounts", layerID)
	graphIDBytes, err := ioutil.ReadFile(filepath.Join(layerRoot, filename))
	if err != nil {
		return err
	}
	removeDir := filepath.Join(root, graphDriver, strings.TrimSpace(string(graphIDBytes)))
	logrus.Debugf("Removing graph directory %s", removeDir)
	if err := os.RemoveAll(removeDir); err != nil {
		return err
	}

	return nil
}

func getAllGraphDrivers(graphDir string) ([]string, error) {
	infos, err := ioutil.ReadDir(graphDir)
	if err != nil {
		return nil, err
	}
	drivers := []string{}
	for _, info := range infos {
		name := info.Name()
		if strings.HasPrefix(name, "repositories-") {
			drivers = append(drivers, name[13:])
		}
	}
	return drivers, nil
}
