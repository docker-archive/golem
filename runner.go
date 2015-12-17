package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Sirupsen/logrus"
	"github.com/dmcgowan/golem/buildutil"
	"github.com/dmcgowan/golem/versionutil"
	"github.com/docker/distribution/digest"
	"github.com/docker/distribution/reference"
	dockerclient "github.com/fsouza/go-dockerclient"
	"github.com/termie/go-shutil"
)

type BaseImageConfiguration struct {
	Base         reference.Named
	ExtraImages  []reference.NamedTagged
	CustomImages []CustomImage

	DockerLoadVersion versionutil.Version
	DockerVersion     versionutil.Version
}

type RunConfiguration struct {
	Name string
	Args []string
	Env  []string
}

type SuiteConfiguration struct {
	Name string
	Path string

	DockerInDocker bool
	BaseImage      BaseImageConfiguration

	Instances []RunConfiguration
}

type RunnerConfiguration struct {
	Suites []SuiteConfiguration

	ExecutableName string
	ExecutablePath string

	ImageNamespace string

	// Swarm whether to run inside of swarm. No
	// local volumes will be used and suite images
	// will first be pushed before running.
	Swarm bool
}

type Runner struct {
	config RunnerConfiguration
	cache  CacheConfiguration
}

func NewRunner(config RunnerConfiguration, cache CacheConfiguration) *Runner {
	return &Runner{
		config: config,
		cache:  cache,
	}
}

func (r *Runner) imageName(name string) string {
	imageName := "golem-" + name + ":latest"
	if r.config.ImageNamespace != "" {
		imageName = path.Join(r.config.ImageNamespace, imageName)
	}
	return imageName
}

func (r *Runner) Build(client DockerClient) error {
	for _, suite := range r.config.Suites {
		baseImage, err := BuildBaseImage(client, suite.BaseImage, r.cache)
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

		// TODO: Move to base image
		buildutil.CopyFile(r.config.ExecutablePath, filepath.Join(td, r.config.ExecutableName), 0755)
		fmt.Fprintf(df, "COPY ./%s /usr/bin/%s\n", r.config.ExecutableName, r.config.ExecutableName)

		logrus.Debugf("Copying %s to %s", suite.Path, filepath.Join(td, "runner"))
		if err := shutil.CopyTree(suite.Path, filepath.Join(td, "runner"), nil); err != nil {
			return fmt.Errorf("error copying test directory: %v", err)
		}

		fmt.Fprintln(df, "COPY ./runner/ /runner")

		if err := df.Close(); err != nil {
			return fmt.Errorf("error closing dockerfile: %s", err)
		}

		builder, err := client.NewBuilder(td, "", r.imageName(suite.Name))
		if err != nil {
			return fmt.Errorf("failed to create builder: %s", err)
		}

		if err := builder.Run(); err != nil {
			return fmt.Errorf("build error: %s", err)
		}
	}
	return nil
}

func (r *Runner) Run(client DockerClient) error {
	// TODO: Run in parallel (use libcompose?)
	// TODO: validate namespace when in swarm mode
	for _, suite := range r.config.Suites {
		for _, instance := range suite.Instances {
			// TODO: Add configuration for nocache
			nocache := false
			contName := "golem-" + suite.Name + "-" + instance.Name

			hc := &dockerclient.HostConfig{
				Privileged: true,
			}

			// TODO: Set arguments for runner
			config := &dockerclient.Config{
				Image:      r.imageName(suite.Name),
				Cmd:        append([]string{fmt.Sprintf("/usr/bin/%s", r.config.ExecutableName)}, instance.Args...),
				Env:        instance.Env,
				WorkingDir: "/runner",
				Volumes: map[string]struct{}{
					"/var/log/docker": struct{}{},
				},
				VolumeDriver: "local",
			}

			if suite.DockerInDocker {
				config.Env = append(config.Env, "DOCKER_GRAPHDRIVER="+getGraphDriver())

				// TODO: In swarm mode, do not use a cached volume
				volumeName := contName + "-graph"
				cont, err := client.InspectContainer(contName)
				if err == nil {
					removeOptions := dockerclient.RemoveContainerOptions{
						ID:            cont.ID,
						RemoveVolumes: true,
					}
					if err := client.RemoveContainer(removeOptions); err != nil {
						return fmt.Errorf("error removing existing container %s: %v", contName, err)
					}
				}

				vol, err := client.InspectVolume(volumeName)
				if err == nil {
					if nocache {
						if err := client.RemoveVolume(vol.Name); err != nil {
							return fmt.Errorf("error removing volume %s: %v", vol.Name, err)
						}
						vol = nil
					}
				}

				if vol == nil {
					createOptions := dockerclient.CreateVolumeOptions{
						Name:   volumeName,
						Driver: "local",
					}
					vol, err = client.CreateVolume(createOptions)
					if err != nil {
						return fmt.Errorf("error creating volume: %v", err)
					}
				}

				logrus.Debugf("Mounting %s to %s", vol.Mountpoint, "/var/lib/docker")
				hc.Binds = append(hc.Binds, fmt.Sprintf("%s:/var/lib/docker", vol.Mountpoint))
			}

			cc := dockerclient.CreateContainerOptions{
				Name:       contName,
				Config:     config,
				HostConfig: hc,
			}

			container, err := client.CreateContainer(cc)
			if err != nil {
				return fmt.Errorf("error creating container: %s", err)
			}

			if err := client.StartContainer(container.ID, hc); err != nil {
				return fmt.Errorf("error starting container: %s", err)
			}

			// TODO: Capture output
			attachOptions := dockerclient.AttachToContainerOptions{
				Container:    container.ID,
				OutputStream: os.Stdout,
				ErrorStream:  os.Stderr,
				Logs:         true,
				Stream:       true,
				Stdout:       true,
				Stderr:       true,
			}
			if err := client.AttachToContainer(attachOptions); err != nil {
				return fmt.Errorf("Error attaching to container: %v", err)
			}
		}
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

func ensureImage(client DockerClient, image string) (string, error) {
	info, err := client.InspectImage(image)
	if err == nil {
		logrus.Debugf("Image found locally %s", image)
		return info.ID, nil
	}
	if err != dockerclient.ErrNoSuchImage {
		logrus.Errorf("Error inspecting image %q: %v", image, err)
		return "", err
	}

	// Image must be tagged reference if it does not exist
	ref, err := reference.Parse(image)
	if err != nil {
		logrus.Debugf("Image is not valid reference %q: %v", image, err)
	}
	tagged, ok := ref.(reference.NamedTagged)
	if !ok {
		logrus.Debugf("Tagged reference required %q", image)
		return "", errors.New("invalid reference, tag needed")
	}

	logrus.Infof("Pulling image %s", tagged.String())

	pullOptions := dockerclient.PullImageOptions{
		Repository:   tagged.Name(),
		Tag:          tagged.Tag(),
		OutputStream: os.Stdout,
	}
	if err := client.PullImage(pullOptions, dockerclient.AuthConfiguration{}); err != nil {
		logrus.Errorf("Error pulling image %q: %v", tagged.String(), err)
		return "", err
	}
	// TODO: Get pulled digest and inspect by digest
	info, err = client.InspectImage(tagged.String())
	if err != nil {
		return "", nil
	}

	return info.ID, nil
}

func saveImage(client DockerClient, filename, imgID string) error {
	// TODO: must not exist
	f, err := os.Create(filename)
	if err != nil {
		return fmt.Errorf("error creating image tar file: %v", err)
	}
	defer f.Close()
	logrus.Debugf("Exporting image %s to %s", imgID, filename)
	ec := dockerclient.ExportImageOptions{
		Name:         imgID,
		OutputStream: f,
	}
	return client.ExportImage(ec)
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
		return fmt.Errorf("error encoding tag map: %v")
	}

	return nil
}

type tag struct {
	Tag   reference.NamedTagged
	Image string
}

type ImageCache struct {
	root string
}

func (ic *ImageCache) imageFile(dgst digest.Digest) string {
	return filepath.Join(ic.root, dgst.Algorithm().String(), dgst.Hex())
}

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

type CustomImage struct {
	Source string
	Target reference.NamedTagged
}

type CacheConfiguration struct {
	ImageCache *ImageCache
	BuildCache buildutil.BuildCache
}

const (
	// hashVersion is used to force build cache
	// busting when the method to compute the
	// hash changes
	hashVersion = "1"
)

// BuildBaseImage builds a base image using the given configuration
// and returns an image id for the given image
func BuildBaseImage(client DockerClient, conf BaseImageConfiguration, c CacheConfiguration) (string, error) {
	tags := []tag{}
	images := []string{}
	for _, ref := range conf.ExtraImages {
		id, err := ensureImage(client, ref.String())
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
		id, err := ensureImage(client, ci.Source)
		if err != nil {
			return "", err
		}
		tags = append(tags, tag{
			Tag:   ci.Target,
			Image: id,
		})

		images = append(images, id)
	}

	dgstr := digest.Canonical.New()
	// Add runner options
	fmt.Fprintf(dgstr.Hash(), "Version: %s\n", hashVersion)
	fmt.Fprintln(dgstr.Hash())
	fmt.Fprintln(dgstr.Hash())

	// TODO: Incorporate image id
	fmt.Fprintf(dgstr.Hash(), "%s\n\n", conf.Base.String())

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

	fmt.Fprintln(dgstr.Hash(), conf.DockerLoadVersion.String())
	fmt.Fprintln(dgstr.Hash(), conf.DockerVersion.String())

	imageHash := dgstr.Digest()

	id, err := c.ImageCache.GetImage(imageHash)
	if err == nil {
		logrus.Debugf("Found image in cache for %s: %s", imageHash, id)
		info, err := client.InspectImage(id)
		if err == nil {
			logrus.Debugf("Cached image found locally %s", info.ID)
			return id, nil
		}
		logrus.Errorf("Unable to find cached image %s: %v", id, err)
	} else {
		logrus.Debugf("Building image, could not find in cache: %v", err)
	}

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

	fmt.Fprintf(df, "FROM %s\n", conf.Base)

	imagesDir := filepath.Join(td, "images")
	if err := os.Mkdir(imagesDir, 0755); err != nil {
		return "", fmt.Errorf("unable to make images directory: %v", err)
	}

	for _, img := range images {
		if err := saveImage(client, filepath.Join(imagesDir, img+".tar"), img); err != nil {
			return "", fmt.Errorf("error saving image %s: %v", img, err)
		}

	}

	if err := saveTagMap(filepath.Join(imagesDir, "images.json"), tags); err != nil {
		return "", fmt.Errorf("error saving tag map: %v", err)
	}

	fmt.Fprintln(df, "COPY ./images /images")

	// Add Docker Binaries (docker test specific)
	if err := c.BuildCache.InstallVersion(conf.DockerVersion, filepath.Join(td, "docker")); err != nil {
		return "", fmt.Errorf("error installing docker version %s: %v", conf.DockerVersion, err)
	}
	if err := c.BuildCache.InstallVersion(conf.DockerLoadVersion, filepath.Join(td, "docker-load")); err != nil {
		return "", fmt.Errorf("error installing docker load version %s: %v", conf.DockerLoadVersion, err)
	}
	fmt.Fprintln(df, "COPY ./docker /usr/bin/docker")
	fmt.Fprintln(df, "COPY ./docker-load /usr/bin/docker-load")
	// TODO: Handle init files

	// Call build
	builder, err := client.NewBuilder(td, "", "")
	if err != nil {
		logrus.Errorf("Error creating builder: %v", err)
		return "", err
	}

	if err := builder.Run(); err != nil {
		logrus.Errorf("Error building: %v", err)
		return "", err
	}

	// Update index
	imageId := builder.ImageID()

	if err := c.ImageCache.SaveImage(imageHash, imageId); err != nil {
		logrus.Errorf("Unable to save image by hash %s: %s", imageHash, imageId)
	}

	return imageId, nil
}
