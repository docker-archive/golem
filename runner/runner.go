// Package runner provides a test runner for running golem test integration suites
package runner

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/net/context"

	"github.com/Sirupsen/logrus"
	"github.com/docker/distribution/digest"
	"github.com/docker/distribution/reference"
	"github.com/docker/docker/pkg/jsonmessage"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/docker/docker/pkg/term"
	"github.com/docker/engine-api/client"
	"github.com/docker/engine-api/types"
	"github.com/docker/engine-api/types/container"
	"github.com/docker/engine-api/types/network"
	"github.com/termie/go-shutil"
)

// BaseImageConfiguration represents the configuration for
// constructing a base image used by a test suite. Every
// container in the test suite will be derived from an
// image created with this configuration.
type BaseImageConfiguration struct {
	Base         reference.Named
	ExtraImages  []reference.NamedTagged
	CustomImages []CustomImage
}

// Script is the configuration for running a command
// including its environment.
type Script struct {
	Command []string `json:"command"`
	Env     []string `json:"env"`
}

// TestScript is a command configuration along with
// expected output for parsing test results.
type TestScript struct {
	Script
	Format string `json:"format"`
}

// RunConfiguration is the all the command
// configurations for running a test instance
// including setup and test commands.
type RunConfiguration struct {
	Setup      []Script     `json:"setup"`
	TestRunner []TestScript `json:"runner"`
}

// InstanceConfiguration is the configuration
// for constructing the test instance container.
type InstanceConfiguration struct {
	RunConfiguration

	Name      string
	BaseImage BaseImageConfiguration
}

// SuiteConfiguration is the configuration for
// a test suite and is used for constructing
// the test suite containers and runtime
// configurations.
type SuiteConfiguration struct {
	Name string
	Path string
	Args []string

	DockerInDocker bool

	Instances []InstanceConfiguration
}

// TestRunner defines an interface for building
// and running a test.
type TestRunner interface {
	Build(DockerClient) error
	Run(DockerClient) error
}

// RunnerConfiguration is the configuration for
// running a set of test suites. This configuration
// determines which suites to run, how the base
// images will be created, and how the test instances
// should be run.
type RunnerConfiguration struct {
	Suites []SuiteConfiguration

	// ExecutableName represents the name of the executable used inside
	// the runner image.
	ExecutableName string

	// Parallel whether to run containers in parallel.
	// No local volumes will be used and suite images
	// will first be pushed before running.
	Parallel bool

	// ManagerImage defines the image which will aggregate
	// the log streams and results
	ManagerImage string

	// ImageNamespace defines the base name of the test images
	// which will be used to push/pull from the test image
	ImageNamespace string
}

// runner represents a golem run session including
// the run configuration information and cache
// information to optimize creation and runtime.
type runner struct {
	config RunnerConfiguration
	cache  CacheConfiguration
	debug  bool
}

// NewRunner creates a new runner from a runner
// and cache configuration.
func NewRunner(config RunnerConfiguration, cache CacheConfiguration, debug bool) TestRunner {
	return &runner{
		config: config,
		cache:  cache,
		debug:  debug,
	}
}

func (r *runner) imageName(name string) string {
	imageName := "golem-" + name + ":latest"
	if r.config.ImageNamespace != "" {
		imageName = path.Join(r.config.ImageNamespace, imageName)
	}
	return imageName
}

// Build builds all suite instance image configured for
// the runner. The result of build will be locally built
// and tagged images ready to push or run directory.
func (r *runner) Build(cli DockerClient) error {
	buildStart := time.Now()

	for _, suite := range r.config.Suites {
		for _, instance := range suite.Instances {
			imageName := r.imageName(instance.Name)
			logrus.WithField("image", imageName).Info("building image")

			baseImage, err := BuildBaseImage(cli, instance.BaseImage, r.cache)
			if err != nil {
				return fmt.Errorf("failure building base image: %v", err)
			}

			// Create temp build directory
			td, err := ioutil.TempDir("", "golem-")
			if err != nil {
				return fmt.Errorf("unable to create tempdir: %v", err)
			}
			defer os.RemoveAll(td)

			// Create Dockerfile in tempDir
			df, err := os.OpenFile(filepath.Join(td, "Dockerfile"), os.O_CREATE|os.O_WRONLY, 0644)
			if err != nil {
				return fmt.Errorf("error creating dockerfile: %v", err)
			}
			defer df.Close()

			fmt.Fprintf(df, "FROM %s\n", baseImage)

			logrus.Debugf("Copying %s to %s", suite.Path, filepath.Join(td, "runner"))
			if err := shutil.CopyTree(suite.Path, filepath.Join(td, "runner"), nil); err != nil {
				return fmt.Errorf("error copying test directory: %v", err)
			}

			fmt.Fprintln(df, "COPY ./runner/ /runner")

			logrus.Debugf("Run configuration: %#v", instance.RunConfiguration)

			instanceF, err := os.Create(filepath.Join(td, "instance.json"))
			if err != nil {
				return fmt.Errorf("error creating instance json file: %s", err)
			}
			if err := json.NewEncoder(instanceF).Encode(instance.RunConfiguration); err != nil {
				instanceF.Close()
				return fmt.Errorf("error encoding configuration: %s", err)
			}
			instanceF.Close()

			fmt.Fprintln(df, "COPY ./instance.json /instance.json")

			if err := df.Close(); err != nil {
				return fmt.Errorf("error closing dockerfile: %s", err)
			}

			builder, err := cli.NewBuilder(td, "", imageName)
			if err != nil {
				return fmt.Errorf("failed to create builder: %s", err)
			}

			if err := builder.Run(); err != nil {
				return fmt.Errorf("build error: %s", err)
			}
		}
	}

	logrus.WithField(timerKey, time.Since(buildStart)).Info("test image build complete")
	return nil
}

// Run starts the test instance containers as well as any
// containers which will manage the tests and waits for
// the results.
func (r *runner) Run(cli DockerClient) error {
	var (
		failedTests int
		runTests    int
		runnerStart = time.Now()
		ctx         = context.Background()
	)

	// TODO: Run in parallel
	// TODO: validate namespace when in parallel mode
	for _, suite := range r.config.Suites {
		for _, instance := range suite.Instances {
			// TODO: Add configuration for nocache
			nocache := false
			contName := "golem-" + instance.Name
			// TODO: Use image ID and not image name
			imageName := r.imageName(instance.Name)

			logFields := logrus.Fields{
				"instance":  instance.Name,
				"image":     imageName,
				"container": contName,
			}
			logrus.WithFields(logFields).Info("running instance")

			hc := &container.HostConfig{
				Privileged:   true,
				VolumeDriver: "local",
			}

			args := []string{}
			if suite.DockerInDocker {
				args = append(args, "-docker")
			}
			if r.debug {
				args = append(args, "-debug")
			}
			// TODO: Add argument for instance name

			config := &container.Config{
				Image:      imageName,
				Cmd:        append([]string{r.config.ExecutableName}, args...),
				WorkingDir: "/runner",
				Volumes: map[string]struct{}{
					"/var/log/docker": {},
				},
			}

			if suite.DockerInDocker {
				config.Env = append(config.Env, "DOCKER_GRAPHDRIVER="+getGraphDriver())

				// TODO: In parallel mode, do not use a cached volume
				volumeName := contName + "-graph"
				cont, err := cli.ContainerInspect(ctx, contName)
				if err == nil {
					removeOptions := types.ContainerRemoveOptions{
						RemoveVolumes: true,
					}
					if err := cli.ContainerRemove(ctx, cont.ID, removeOptions); err != nil {
						return fmt.Errorf("error removing existing container %s: %v", contName, err)
					}
				}

				var createVolume bool
				vol, err := cli.VolumeInspect(ctx, volumeName)
				if err == nil {
					if nocache {
						if err := cli.VolumeRemove(ctx, vol.Name); err != nil {
							return fmt.Errorf("error removing volume %s: %v", vol.Name, err)
						}
						createVolume = true
					}
				} else if client.IsErrVolumeNotFound(err) {
					createVolume = true
				} else {
					return fmt.Errorf("error inspecting volume: %v", err)
				}

				if createVolume {
					createOptions := types.VolumeCreateRequest{
						Name:   volumeName,
						Driver: "local",
					}
					vol, err = cli.VolumeCreate(ctx, createOptions)
					if err != nil {
						return fmt.Errorf("error creating volume: %v", err)
					}
				}

				// TODO: Use volume name instead of mountpoint
				logrus.Debugf("Mounting %s to %s", vol.Mountpoint, "/var/lib/docker")
				hc.Binds = append(hc.Binds, fmt.Sprintf("%s:/var/lib/docker", vol.Mountpoint))
			}

			nc := &network.NetworkingConfig{}

			container, err := cli.ContainerCreate(ctx, config, hc, nc, contName)
			if err != nil {
				return fmt.Errorf("error creating container: %s", err)
			}

			for _, warning := range container.Warnings {
				logrus.Warnf("Container %q create warning: %v", contName, warning)
			}

			if err := cli.ContainerStart(ctx, container.ID); err != nil {
				return fmt.Errorf("error starting container: %s", err)
			}

			attachOptions := types.ContainerAttachOptions{
				Stream: true,
				Stdout: true,
				Stderr: true,
			}
			resp, err := cli.ContainerAttach(ctx, container.ID, attachOptions)
			if err != nil {
				return fmt.Errorf("Error attaching to container: %v", err)
			}

			// TODO: Capture output for parallel mode
			if _, err := stdcopy.StdCopy(os.Stdout, os.Stderr, resp.Reader); err != nil {
				return fmt.Errorf("Error copying output stream: %v", err)
			}

			inspectedContainer, err := cli.ContainerInspect(ctx, container.ID)
			if err != nil {
				return fmt.Errorf("Error inspecting container: %v", err)
			}
			runTests = runTests + 1
			if inspectedContainer.State.ExitCode > 0 {
				logrus.Errorf("Test failed with exit code %d", inspectedContainer.State.ExitCode)
				failedTests = failedTests + 1
			}
		}
	}

	logFields := logrus.Fields{
		timerKey: time.Since(runnerStart),
		"ran":    runTests,
		"failed": failedTests,
	}
	logrus.WithFields(logFields).Info("test runner complete")

	if failedTests > 0 {
		return fmt.Errorf("test failure: %d of %d tests failed", failedTests, runTests)
	}

	return nil
}

func getGraphDriver() string {
	d := os.Getenv("DOCKER_GRAPHDRIVER")
	switch d {
	case "":
		return "overlay"
	default:
		return d
	}
}

func registryAuthNotSupported() (string, error) {
	return "", errors.New("Registry auth not supported, pull image and re-run golem")
}

func ensureImage(cli DockerClient, image string) (string, error) {
	ctx := context.Background()
	info, _, err := cli.ImageInspectWithRaw(ctx, image, false)
	if err == nil {
		logrus.Debugf("Image found locally %s", image)
		return info.ID, nil
	}

	if !client.IsErrImageNotFound(err) {
		logrus.Errorf("Error inspecting image %q: %v", image, err)
		return "", err
	}

	// Image must be tagged reference if it does not exist
	ref, err := reference.Parse(image)
	if err != nil {
		logrus.Errorf("Image is not valid reference %q: %v", image, err)
		return "", err
	}
	tagged, ok := ref.(reference.NamedTagged)
	if !ok {
		logrus.Errorf("Tagged reference required %q", image)
		return "", errors.New("invalid reference, tag needed")
	}

	pullStart := time.Now()
	pullOptions := types.ImagePullOptions{
		PrivilegeFunc: registryAuthNotSupported,
	}
	resp, err := cli.ImagePull(ctx, tagged.String(), pullOptions)
	if err != nil {
		logrus.Errorf("Error pulling image %q: %v", tagged.String(), err)
		return "", err
	}
	defer resp.Close()

	outFd, isTerminalOut := term.GetFdInfo(os.Stdout)

	if err = jsonmessage.DisplayJSONMessagesStream(resp, os.Stdout, outFd, isTerminalOut, nil); err != nil {
		logrus.Errorf("Error copying pull output: %v", err)
		return "", err
	}
	// TODO: Get pulled digest

	logFields := logrus.Fields{
		timerKey: time.Since(pullStart),
		"image":  tagged.String(),
	}
	logrus.WithFields(logFields).Info("image pulled")

	info, _, err = cli.ImageInspectWithRaw(ctx, tagged.String(), false)
	if err != nil {
		return "", nil
	}

	return info.ID, nil
}

func saveImage(cli DockerClient, filename, imgID string) error {
	ctx := context.Background()

	// TODO: must not exist
	f, err := os.Create(filename)
	if err != nil {
		return fmt.Errorf("error creating image tar file: %v", err)
	}
	defer f.Close()
	logrus.Debugf("Exporting image %s to %s", imgID, filename)

	r, err := cli.ImageSave(ctx, []string{imgID})
	if err != nil {
		return fmt.Errorf("error calling save image: %v", err)
	}
	defer r.Close()

	if _, err = io.Copy(f, r); err != nil {
		return fmt.Errorf("error copying saved image response: %v", err)
	}

	return nil
}

func saveTagMap(filename string, tags []tag) error {
	m := map[string][]string{}
	for _, t := range tags {
		m[t.Image] = append(m[t.Image], t.Tag.String())
	}

	mf, err := os.Create(filename)
	if err != nil {
		return fmt.Errorf("error opening images.json file: %v", err)
	}
	defer mf.Close()

	if err := json.NewEncoder(mf).Encode(m); err != nil {
		return fmt.Errorf("error encoding tag map: %v", err)
	}

	return nil
}

type tag struct {
	Tag   reference.NamedTagged
	Image string
}

// ImageCache reprsents a cache for mapping digests
// to image ids. This can be used to create a custom
// image build cache based on a digest from instructions.
type ImageCache struct {
	root string
}

// NewImageCache creates an image cache at the provided root.
func NewImageCache(root string) *ImageCache {
	return &ImageCache{
		root: root,
	}
}

func (ic *ImageCache) imageFile(dgst digest.Digest) string {
	return filepath.Join(ic.root, dgst.Algorithm().String(), dgst.Hex())
}

// GetImage gets an image id with the associated digest from the cache.
func (ic *ImageCache) GetImage(dgst digest.Digest) (string, error) {
	f, err := os.Open(ic.imageFile(dgst))
	if err != nil {
		// TODO: Detect does not exist error and return const error
		return "", err
	}
	defer f.Close()

	b, err := ioutil.ReadAll(f)
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(string(b)), nil
}

// SaveImage saves the associated id mapping to the provided digest.
// This allows the image cache to act as a client side build cache.
func (ic *ImageCache) SaveImage(dgst digest.Digest, id string) error {
	fp := ic.imageFile(dgst)
	if err := os.MkdirAll(filepath.Dir(fp), 0755); err != nil {
		return err
	}
	f, err := os.Create(fp)
	if err != nil {
		return err
	}
	defer f.Close()

	if _, err := fmt.Fprintf(f, "%s", id); err != nil {
		return err
	}
	logrus.Debugf("Saved %s->%s at %s", dgst, id, fp)
	return nil
}

// CustomImage represents an image which will exist in a test
// container with a given name and exported from another
// Docker instance with the source image name.
type CustomImage struct {
	Source      string
	Target      reference.NamedTagged
	Version     string
	DefaultOnly bool
}

func (ci CustomImage) String() string {
	if ci.Version == "" {
		return fmt.Sprintf("%s,%s", ci.Target.String(), ci.Source)
	}
	return fmt.Sprintf("%s,%s,%s", ci.Target.String(), ci.Source, ci.Version)
}

// CacheConfiguration represents a cache configuration for
// custom image cache for locally built images.
type CacheConfiguration struct {
	ImageCache *ImageCache
}

const (
	// hashVersion is used to force build cache
	// busting when the method to compute the
	// hash changes
	hashVersion = "1"
)

func nameToEnv(name string) string {
	name = strings.Replace(name, ".", "_", -1)
	name = strings.Replace(name, "-", "_", -1)
	name = strings.Replace(name, ":", "_", -1)
	return strings.ToUpper(name)
}

// BuildBaseImage builds a base image using the given configuration
// and returns an image id for the given image
func BuildBaseImage(cli DockerClient, conf BaseImageConfiguration, c CacheConfiguration) (string, error) {
	ctx := context.Background()
	tags := []tag{}
	images := []string{}
	envs := []string{}

	baseImageID, err := ensureImage(cli, conf.Base.String())
	if err != nil {
		return "", err
	}

	for _, ref := range conf.ExtraImages {
		id, err := ensureImage(cli, ref.String())
		if err != nil {
			return "", err
		}
		tags = append(tags, tag{
			Tag:   ref,
			Image: id,
		})
		images = append(images, id)
	}
	for _, ci := range conf.CustomImages {
		id, err := ensureImage(cli, ci.Source)
		if err != nil {
			return "", err
		}
		tags = append(tags, tag{
			Tag:   ci.Target,
			Image: id,
		})

		envs = append(envs, fmt.Sprintf("%s_VERSION %s", nameToEnv(ci.Target.Name()), ci.Version))

		images = append(images, id)
	}

	dgstr := digest.Canonical.New()
	// Add runner options
	fmt.Fprintf(dgstr.Hash(), "Version: %s\n\n", hashVersion)

	fmt.Fprintf(dgstr.Hash(), "%s\n\n", baseImageID)

	imageTags := map[string]string{}
	allTags := []string{}
	for _, t := range tags {
		imageTags[t.Tag.String()] = t.Image
		allTags = append(allTags, t.Tag.String())
	}
	sort.Strings(allTags)
	for _, t := range allTags {
		fmt.Fprintf(dgstr.Hash(), "%s %s\n", t, imageTags[t])
	}

	fmt.Fprintln(dgstr.Hash())

	// Version environment variable
	sort.Strings(envs)

	fmt.Fprintln(dgstr.Hash())
	fmt.Fprintln(dgstr.Hash(), strings.Join(envs, " "))

	imageHash := dgstr.Digest()

	// TODO: Use step by step image cache instead of single image cache
	id, err := c.ImageCache.GetImage(imageHash)
	if err == nil {
		logrus.Debugf("Found image in cache for %s: %s", imageHash, id)
		info, _, err := cli.ImageInspectWithRaw(ctx, id, false)
		if err == nil {
			logrus.Debugf("Cached image found locally %s", info.ID)
			return id, nil
		}
		logrus.Errorf("Unable to find cached image %s: %v", id, err)
	} else {
		logrus.Debugf("Building image, could not find in cache: %v", err)
	}

	buildStart := time.Now()

	// Create temp build directory
	td, err := ioutil.TempDir("", "golem-")
	if err != nil {
		return "", fmt.Errorf("unable to create tempdir: %s", err)
	}
	defer os.RemoveAll(td)

	// Create Dockerfile in tempDir
	df, err := os.OpenFile(filepath.Join(td, "Dockerfile"), os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return "", fmt.Errorf("unable to create dockerfile: %s", err)
	}
	defer df.Close()

	fmt.Fprintf(df, "FROM %s\n", baseImageID)

	imagesDir := filepath.Join(td, "images")
	if err := os.Mkdir(imagesDir, 0755); err != nil {
		return "", fmt.Errorf("unable to make images directory: %v", err)
	}

	saveStart := time.Now()
	logrus.Debugf("Saving %d images", len(images))
	for _, img := range images {
		if err := saveImage(cli, filepath.Join(imagesDir, img+".tar"), img); err != nil {
			return "", fmt.Errorf("error saving image %s: %v", img, err)
		}

	}
	logFields := logrus.Fields{
		timerKey: time.Since(saveStart),
		"images": len(images),
	}
	logrus.WithFields(logFields).Info("image save complete")

	if err := saveTagMap(filepath.Join(imagesDir, "images.json"), tags); err != nil {
		return "", fmt.Errorf("error saving tag map: %v", err)
	}

	fmt.Fprintln(df, "COPY ./images /images")

	for _, e := range envs {
		fmt.Fprintf(df, "ENV %s\n", e)
	}

	// Call build
	builder, err := cli.NewBuilder(td, "", "")
	if err != nil {
		logrus.Errorf("Error creating builder: %v", err)
		return "", err
	}

	if err := builder.Run(); err != nil {
		logrus.Errorf("Error building: %v", err)
		return "", err
	}

	logrus.WithField(timerKey, time.Since(buildStart)).Info("base image build complete")

	// Update index
	imageID := builder.ImageID()

	if err := c.ImageCache.SaveImage(imageHash, imageID); err != nil {
		logrus.Errorf("Unable to save image by hash %s: %s", imageHash, imageID)
	}

	return imageID, nil
}
