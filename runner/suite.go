package runner

import (
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/net/context"

	"github.com/Sirupsen/logrus"
	"github.com/docker/distribution/reference"
	"github.com/docker/docker/pkg/jsonmessage"
	"github.com/docker/docker/pkg/term"
	"github.com/docker/engine-api/client"
	"github.com/docker/engine-api/types"
	"github.com/docker/golem/clientutil"
	"github.com/docker/golem/versionutil"
)

const (
	timerKey = "elapsed"
)

// SuiteRunnerConfiguration is the configuration for running
// a test inside the suite instance container.
type SuiteRunnerConfiguration struct {
	DockerInDocker        bool
	CleanDockerGraph      bool
	CleanImageCache       bool
	DockerLoadLogCapturer LogCapturer
	DockerLogCapturer     LogCapturer

	ComposeFile     string
	ComposeCapturer LogCapturer

	RunConfiguration RunConfiguration
	SetupLogCapturer LogCapturer
	TestCapturer     LogCapturer
}

// SuiteRunner is the runtime manager for the test
// inside the suite instance container.
type SuiteRunner struct {
	config SuiteRunnerConfiguration

	daemonCloser func() error
}

// NewSuiteRunner creates a new SuiteRunner with the provided
// suite runner configuration.
func NewSuiteRunner(config SuiteRunnerConfiguration) *SuiteRunner {
	return &SuiteRunner{
		config: config,
	}
}

// Setup does the test setup for the suite. This includes importing
// any docker images, running setup scripts, and starting the docker
// daemon used by the tests.
func (sr *SuiteRunner) Setup() error {
	ctx := context.Background()
	setupStart := time.Now()
	// Run all setup scripts
	for _, setupScript := range sr.config.RunConfiguration.Setup {
		if err := RunScript(sr.config.SetupLogCapturer, setupScript); err != nil {
			return fmt.Errorf("error running setup script %s: %s", setupScript.Command[0], err)
		}
	}

	// Start Docker-in-Docker daemon for tests, build compose images
	if sr.config.DockerInDocker {
		if sr.config.CleanDockerGraph {
			// Check if empty
			info, err := ioutil.ReadDir("/var/lib/docker")
			if err != nil {
				return fmt.Errorf("error reading /var/lib/docker: %v", err)
			}

			for _, fInfo := range info {
				cleanFile := filepath.Join("/var/lib/docker", fInfo.Name())
				if err := os.RemoveAll(cleanFile); err != nil {
					return fmt.Errorf("error cleaning %s: %s", cleanFile, err)
				}
			}
		}

		dockerStart := time.Now()
		logrus.Debugf("Starting daemon")
		pc, k, err := StartDaemon(ctx, "docker", sr.config.DockerLogCapturer)
		if err != nil {
			return fmt.Errorf("error starting daemon: %s", err)
		}
		sr.daemonCloser = k
		logrus.WithField(timerKey, time.Since(dockerStart)).Info("docker daemon startup complete")

		cleanupStart := time.Now()
		// Remove all containers
		containers, err := pc.ContainerList(ctx, types.ContainerListOptions{All: true})
		if err != nil {
			return fmt.Errorf("error listing containers: %v", err)
		}
		for _, container := range containers {
			logrus.Debugf("Removing container %s", container.ID)
			removeOptions := types.ContainerRemoveOptions{
				RemoveVolumes: true,
				Force:         true,
			}
			if err := pc.ContainerRemove(ctx, container.ID, removeOptions); err != nil {
				return fmt.Errorf("error removing container: %v", err)
			}
		}

		if err := syncImages(ctx, pc, "/images", sr.config.CleanImageCache); err != nil {
			return fmt.Errorf("error syncing images: %v", err)
		}
		logrus.WithField(timerKey, time.Since(cleanupStart)).Info("image sync complete")

		if sr.config.ComposeFile != "" {
			logrus.Debugf("Build compose images")
			buildStart := time.Now()
			buildArgs := []string{"docker-compose", "-f", sr.config.ComposeFile, "build"}
			if sr.config.CleanImageCache {
				buildArgs = append(buildArgs, "--no-cache")
			}
			buildScript := Script{
				Command: buildArgs,
				Env:     os.Environ(),
			}
			if err := RunScript(sr.config.ComposeCapturer, buildScript); err != nil {
				return fmt.Errorf("error running docker compose build: %v", err)
			}
			logrus.WithField(timerKey, time.Since(buildStart)).Info("compose build complete")
			logrus.Debugf("Starting compose containers")
			upStart := time.Now()
			upScript := Script{
				Command: []string{"docker-compose", "-f", sr.config.ComposeFile, "up", "-d"},
				Env:     os.Environ(),
			}

			if err := RunScript(sr.config.ComposeCapturer, upScript); err != nil {
				return fmt.Errorf("error running docker compose up: %v", err)
			}
			logrus.WithField(timerKey, time.Since(upStart)).Info("compose up complete")

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

	logrus.WithField(timerKey, time.Since(setupStart)).Info("setup complete")

	return nil
}

// TearDown releases on test resources and stops any running containers
// docker daemon.
func (sr *SuiteRunner) TearDown() (err error) {
	tearDownStart := time.Now()
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

	logrus.WithField(timerKey, time.Since(tearDownStart)).Info("teardown complete")

	return
}

// RunTests runs the tests in order, capturing any output to
// the test capturer.
// TODO: Parse output and send to a test result manager.
func (sr *SuiteRunner) RunTests() error {
	runnerStart := time.Now()
	for _, runner := range sr.config.RunConfiguration.TestRunner {
		cmd := exec.Command(runner.Command[0], runner.Command[1:]...)
		// TODO: Parse Stdout using sr.config.RunConfiguration.TestRunner.Format
		cmd.Stdout = sr.config.TestCapturer.Stdout()
		cmd.Stderr = sr.config.TestCapturer.Stderr()
		cmd.Env = append(os.Environ(), runner.Env...)
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("run error: %s", err)
		}
	}

	logrus.WithField(timerKey, time.Since(runnerStart)).Info("suite runner complete")

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
func StartDaemon(ctx context.Context, binary string, lc LogCapturer) (DockerClient, func() error, error) {
	// Get Docker version of process
	previousVersion, err := versionutil.BinaryVersion(binary)
	if err != nil {
		return DockerClient{}, nil, fmt.Errorf("could not get binary version: %s", err)
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
		return DockerClient{}, nil, fmt.Errorf("could not start daemon: %s", err)
	}

	logrus.Debugf("Waiting for daemon to start")
	time.Sleep(2 * time.Second)

	cli, err := client.NewEnvClient()
	if err != nil {
		return DockerClient{}, nil, fmt.Errorf("could not initialize client: %s", err)
	}

	// Wait for it to start
	for i := 0; ; i++ {
		v, err := cli.ServerVersion(ctx)
		if err == nil {
			logrus.Debugf("Established connection to daemon with version %s", v.Version)
			break
		}
		if i >= 10 {
			logrus.Fatalf("Failed to establish connection to daemon, check logs, quitting")
		}
		time.Sleep(time.Second)
	}

	kill := func() error {
		if err := cmd.Process.Kill(); err != nil {
			return err
		}
		time.Sleep(500 * time.Millisecond)
		return os.RemoveAll("/var/run/docker.pid")
	}

	return DockerClient{Client: cli, options: &clientutil.ClientOptions{}}, kill, nil
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

func syncImages(ctx context.Context, cli DockerClient, imageRoot string, clean bool) error {
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

	images, err := cli.ImageList(ctx, types.ImageListOptions{})
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
				if err := tagImage(ctx, cli, img.ID, t); err != nil {
					return err
				}
			}
			for _, t := range removedTags {
				// Check if this image tag conflicts with an expected
				// tag, in which case force tag will update
				if _, ok := allTags[t]; !ok {
					if clean {
						logrus.Debugf("Removing tag %s", t)
						if _, err := cli.ImageRemove(ctx, t, types.ImageRemoveOptions{}); err != nil {
							return fmt.Errorf("error removing tag %s: %v", t, err)
						}
					} else {
						logrus.Debugf("Keeping tag: %s", t)
					}
				}
			}
		} else if clean {
			removeOptions := types.ImageRemoveOptions{
				Force: true,
			}
			if _, err := cli.ImageRemove(ctx, img.ID, removeOptions); err != nil {
				return fmt.Errorf("error moving image %s: %v", img.ID, err)
			}
		} else {
			logrus.Debugf("Keeping image %s with tags %v", img.ID, img.RepoTags)
		}

	}

	for imageID := range neededImages {
		tags, ok := m[imageID]
		if !ok {
			return fmt.Errorf("missing image %s in tag map", imageID)
		}
		_, _, err := cli.ImageInspectWithRaw(ctx, imageID, false)
		if err != nil {
			if err := imageLoad(ctx, cli, imageRoot, imageID); err != nil {
				return err
			}
		}
		for _, t := range tags {
			if err := tagImage(ctx, cli, imageID, t); err != nil {
				return err
			}
		}
	}

	return nil
}

func imageLoad(ctx context.Context, cli DockerClient, imageRoot, imageID string) error {
	tf, err := os.Open(filepath.Join(imageRoot, imageID+".tar"))
	if err != nil {
		return fmt.Errorf("error opening image tar %s: %v", imageID, err)
	}
	defer tf.Close()

	resp, err := cli.ImageLoad(ctx, tf, true)
	if err != nil {
		return fmt.Errorf("error loading image %s: %v", imageID, err)
	}
	defer resp.Body.Close()

	outFd, isTerminalOut := term.GetFdInfo(os.Stdout)

	if resp.Body != nil && resp.JSON {
		return jsonmessage.DisplayJSONMessagesStream(resp.Body, os.Stdout, outFd, isTerminalOut, nil)
	}

	_, err = io.Copy(os.Stdout, resp.Body)
	return err
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

func tagImage(ctx context.Context, cli DockerClient, img, tag string) error {
	ref, err := reference.Parse(tag)
	if err != nil {
		return fmt.Errorf("invalid tag %s: %v", tag, err)
	}
	namedTagged, ok := ref.(reference.NamedTagged)
	if !ok {
		return fmt.Errorf("expecting named tagged reference: %s", tag)
	}
	tagOptions := types.ImageTagOptions{
		Force: true,
	}
	if err := cli.ImageTag(ctx, img, namedTagged.String(), tagOptions); err != nil {
		return fmt.Errorf("error tagging image %s as %s: %v", img, tag, err)
	}

	return nil
}
