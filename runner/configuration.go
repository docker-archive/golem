package runner

// Suite Configuration Ordering
// Golem defaults
// Suite directory golem.conf
// Run directory golem.conf
// Environment variables
// Command line flags

import (
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/Sirupsen/logrus"
	"github.com/bugsnag/osext"
	"github.com/docker/distribution/reference"
	"github.com/docker/golem/versionutil"
)

var globalDefault resolver

func init() {
	cwd, err := os.Getwd()
	if err != nil {
		panic(err)
	}

	globalDefault = defaultResolver{
		base: assertTagged("distribution/golem-runner:0.1-bats"),
		path: cwd,
	}
}

// TODO: Just make this an array, no need to de-duplicate here
type customImageMap map[string]CustomImage

func (m customImageMap) String() string {
	values := []string{}
	for k, v := range m {
		values = append(values, fmt.Sprintf("%s,%s", k, v))
	}
	return strings.Join(values, " ")
}

func (m customImageMap) Set(value string) error {
	parts := strings.Split(value, ",")
	if len(parts) < 2 || len(parts) > 3 {
		return errors.New("invalid custom image format, expected \"name,reference[,version]\"")
	}
	ref, err := reference.Parse(parts[0])
	if err != nil {
		return err
	}
	namedTagged, ok := ref.(reference.NamedTagged)
	if !ok {
		return fmt.Errorf("reference %s must contain name and tag", ref.String())
	}
	source, err := reference.ParseNamed(parts[1])
	if err != nil {
		return err
	}

	var version string
	if len(parts) == 3 {
		version = parts[2]
	} else if refTag, ok := source.(reference.Tagged); ok {
		version = refTag.Tag()
	} else {
		// TODO: In this case is it better to leave it blank and use the default
		// from the configuration file?
		version = namedTagged.Tag()
	}

	key := fmt.Sprintf("%s,%s", parts[0], parts[1])
	m[key] = CustomImage{
		Source:  source.String(),
		Target:  namedTagged,
		Version: version,
	}

	return nil
}

type configurationVersion versionutil.Version

func (v *configurationVersion) String() string {
	return versionutil.Version(*v).String()
}

func (v *configurationVersion) Set(value string) error {
	dv, err := versionutil.ParseVersion(value)
	if err != nil {
		return err
	}
	*v = configurationVersion(dv)

	return nil
}

type testSuite struct {
	name string
	path string
}

type suites map[string]string

func (s suites) String() string {
	names := []string{}
	for name := range s {
		names = append(names, name)
	}
	return strings.Join(names, ",")
}

func (s suites) Set(value string) error {
	absPath, err := filepath.Abs(value)
	if err != nil {
		return err
	}
	if info, err := os.Stat(absPath); err != nil {
		return err
	} else if !info.IsDir() {
		return errors.New("expecting suite to be given as directory")
	}

	s[filepath.Base(filepath.Dir(absPath))] = absPath

	return nil
}

// ConfigurationManager manages flags and resolving configuration
// settings into a runner configuration.
type ConfigurationManager struct {
	flagResolver *flagResolver
	suites       suites
}

// NewConfigurationManager creates a new configuration manager
// and registers associated flags.
func NewConfigurationManager() *ConfigurationManager {
	m := &ConfigurationManager{
		flagResolver: newFlagResolver(),
	}

	// TODO: support extra images
	flag.Var(m.suites, "s", "Path to test suite to run")

	return m
}

// CreateRunner creates a new test runner from a docker load version
// and cache configuration.
func (c *ConfigurationManager) CreateRunner(cache CacheConfiguration) (TestRunner, error) {
	runConfig, err := c.runnerConfiguration()
	if err != nil {
		return nil, err
	}
	return newRunner(runConfig, cache), nil
}

// runnerConfiguration creates a runnerConfiguration resolving all the
// configurations from command line and provided configuration files.
func (c *ConfigurationManager) runnerConfiguration() (runnerConfiguration, error) {
	// TODO: eliminate suites and just use arguments
	var conf string
	// Get first flag
	if flag.NArg() == 0 {
		cwd, err := os.Getwd()
		if err != nil {
			return runnerConfiguration{}, err
		}
		conf = filepath.Join(cwd, "golem.conf")
		logrus.Debugf("No configuration given, trying current directory %s", conf)
	} else {
		absPath, err := filepath.Abs(flag.Arg(0))
		if err != nil {
			return runnerConfiguration{}, err
		}

		info, err := os.Stat(absPath)
		if err != nil {
			return runnerConfiguration{}, err
		}
		if info.IsDir() {
			absPath = filepath.Join(absPath, "golem.conf")
			if _, err := os.Stat(absPath); err != nil {
				return runnerConfiguration{}, err

			}
		}
		conf = absPath

	}

	suites, err := parseSuites(flag.Args())
	if err != nil {
		return runnerConfiguration{}, err
	}

	// TODO: Support non-linux by downloading and replacing executable path
	executablePath, err := osext.Executable()
	if err != nil {
		return runnerConfiguration{}, fmt.Errorf("error getting path to executable: %s", err)
	}

	runnerConfig := runnerConfiguration{
		ExecutableName: "golem_runner",
		ExecutablePath: executablePath,
	}

	for _, suite := range suites {
		resolver := newMultiResolver(c.flagResolver, suite, globalDefault)

		registrySuite := SuiteConfiguration{
			Name:           resolver.Name(),
			Path:           resolver.Path(),
			DockerInDocker: resolver.Dind(),
		}

		baseConf := BaseImageConfiguration{
			Base:        resolver.BaseImage(),
			ExtraImages: resolver.Images(),
		}

		runConfig := resolver.RunConfiguration()
		imageMatrix := expandCustomImageMatrix(resolver.CustomImages())

		var multiInstance bool
		if len(imageMatrix) > 1 {
			logrus.Debugf("Running %d instance for suite %s", len(imageMatrix), registrySuite.Name)
			multiInstance = true
		}

		if len(imageMatrix) == 0 {
			conf := InstanceConfiguration{
				Name:             registrySuite.Name,
				BaseImage:        baseConf,
				RunConfiguration: runConfig,
			}
			registrySuite.Instances = append(registrySuite.Instances, conf)
		} else {
			for idx, customImages := range imageMatrix {
				name := registrySuite.Name
				if multiInstance {
					logrus.Debugf("Instance %d: %v", idx+1, customImages)
					name = fmt.Sprintf("%s-%d", name, idx+1)
				}
				imageConf := baseConf
				imageConf.CustomImages = customImages

				conf := InstanceConfiguration{
					Name:             name,
					BaseImage:        imageConf,
					RunConfiguration: runConfig,
				}
				registrySuite.Instances = append(registrySuite.Instances, conf)
			}
		}

		runnerConfig.Suites = append(runnerConfig.Suites, registrySuite)
	}

	return runnerConfig, nil
}

// resolver is an interface for getting test configurations
// from a configuration setting.
type resolver interface {
	Name() string
	Path() string
	BaseImage() reference.NamedTagged
	Dind() bool
	Images() []reference.NamedTagged
	RunConfiguration() RunConfiguration
	CustomImages() []CustomImage
}

type flagResolver struct {
	customImages customImageMap
}

func newFlagResolver() *flagResolver {
	fr := &flagResolver{
		customImages: customImageMap{},
	}

	flag.Var(fr.customImages, "i", "Set a custom image for running tests")

	return fr
}

func (fr *flagResolver) Name() string {
	return ""
}

func (fr *flagResolver) Path() string {
	return ""
}

func (fr *flagResolver) BaseImage() reference.NamedTagged {
	return nil
}

func (fr *flagResolver) Dind() bool {
	return false
}

func (fr *flagResolver) Images() []reference.NamedTagged {
	return nil
}

func (fr *flagResolver) RunConfiguration() RunConfiguration {
	return RunConfiguration{}
}

func (fr *flagResolver) CustomImages() []CustomImage {
	customImages := make([]CustomImage, 0, len(fr.customImages))
	for _, ci := range fr.customImages {
		customImages = append(customImages, ci)
	}
	return customImages
}

// defaultResolver is used to inject defaults
type defaultResolver struct {
	base reference.NamedTagged
	path string
}

func (dr defaultResolver) Name() string {
	return "default"
}

func (dr defaultResolver) Path() string {
	return dr.path
}

func (dr defaultResolver) BaseImage() reference.NamedTagged {
	return dr.base
}

func (dr defaultResolver) Dind() bool {
	return false
}

func (dr defaultResolver) Images() []reference.NamedTagged {
	return nil
}

func (dr defaultResolver) RunConfiguration() RunConfiguration {
	return RunConfiguration{}
}

func (dr defaultResolver) CustomImages() []CustomImage {
	return nil
}

type multiResolver struct {
	resolvers []resolver
}

func newMultiResolver(resolver ...resolver) resolver {
	return multiResolver{
		resolvers: resolver,
	}
}

func (mr multiResolver) Name() string {
	// Return first non-empty value
	for _, r := range mr.resolvers {
		if name := r.Name(); name != "" {
			return name
		}
	}
	return ""
}

func (mr multiResolver) Path() string {
	// Return first non-empty value
	for _, r := range mr.resolvers {
		if path := r.Path(); path != "" {
			return path
		}
	}
	return ""
}

func (mr multiResolver) BaseImage() reference.NamedTagged {
	for _, r := range mr.resolvers {
		if base := r.BaseImage(); base != nil {
			return base
		}
	}
	return nil
}

func (mr multiResolver) Dind() bool {
	// True if any resolve returns true
	for _, r := range mr.resolvers {
		if r.Dind() {
			return true
		}
	}
	return len(mr.Images()) > 0
}

func (mr multiResolver) Images() []reference.NamedTagged {
	imageSet := map[string]reference.NamedTagged{}
	// Merge all sets
	for _, r := range mr.resolvers {
		for _, named := range r.Images() {
			imageSet[named.String()] = named
		}
	}
	images := make([]reference.NamedTagged, 0, len(imageSet))
	for _, named := range imageSet {
		images = append(images, named)
	}
	return images
}

func (mr multiResolver) RunConfiguration() RunConfiguration {
	runConfig := RunConfiguration{}
	for _, r := range mr.resolvers {
		rc := r.RunConfiguration()
		runConfig.Setup = append(runConfig.Setup, rc.Setup...)
		runConfig.TestRunner = append(runConfig.TestRunner, rc.TestRunner...)
	}
	return runConfig
}

func (mr multiResolver) CustomImages() []CustomImage {
	var customImages []CustomImage
	targets := map[string]struct{}{}
	for _, r := range mr.resolvers {
		for _, customImage := range r.CustomImages() {
			if customImage.DefaultOnly {
				targets[customImage.Target.String()] = struct{}{}
			}
			var hasImage bool
			for i, existingImage := range customImages {
				if customImage.Target.String() == existingImage.Target.String() {
					if existingImage.DefaultOnly {
						customImages[i] = customImage
						hasImage = true
					}
					if customImage.DefaultOnly || (customImage.Source == existingImage.Source && customImage.Version == existingImage.Version) {
						hasImage = true
					}
					if hasImage {
						break
					}
				}
			}
			if !hasImage {
				customImages = append(customImages, customImage)
			}
		}
	}
	filtered := make([]CustomImage, 0, len(customImages))
	for _, customImage := range customImages {
		if _, ok := targets[customImage.Target.String()]; ok {
			filtered = append(filtered, customImage)
		}
	}
	return filtered

}

// configurationSuite represents the configuration for
// an entire test suite. The test suite may have multiple
// instances
type configurationSuite struct {
	config suiteConfiguration

	path         string
	base         reference.NamedTagged
	images       []reference.NamedTagged
	customImages []CustomImage

	resolvedName string
}

func (cs *configurationSuite) SetName(name string) {
	cs.resolvedName = name
}

func (cs *configurationSuite) Name() string {
	return cs.resolvedName
}

func (cs *configurationSuite) Path() string {
	return cs.path
}

func (cs *configurationSuite) BaseImage() reference.NamedTagged {
	return cs.base
}

func (cs *configurationSuite) Dind() bool {
	return cs.config.Dind
}

func (cs *configurationSuite) Images() []reference.NamedTagged {
	return cs.images
}

func (cs *configurationSuite) RunConfiguration() RunConfiguration {
	runConfig := RunConfiguration{}
	for _, script := range cs.config.Pretest {
		// TODO: respect quoted values
		command := strings.Split(script.Command, " ")
		runConfig.Setup = append(runConfig.Setup, Script{
			Command: command,
			Env:     script.Env,
		})
	}
	for _, script := range cs.config.Runner {
		// TODO: respect quoted values
		command := strings.Split(script.Command, " ")
		runConfig.TestRunner = append(runConfig.TestRunner, TestScript{
			Script: Script{
				Command: command,
				Env:     script.Env,
			},
			Format: script.Format,
		})
	}

	return runConfig
}

func (cs *configurationSuite) CustomImages() []CustomImage {
	return cs.customImages
}

func newSuiteConfiguration(path string, config suiteConfiguration) (*configurationSuite, error) {
	customImages := make([]CustomImage, 0, len(config.CustomImages))
	for _, value := range config.CustomImages {
		ref, err := reference.Parse(value.Tag)
		if err != nil {
			return nil, err
		}
		target, ok := ref.(reference.NamedTagged)
		if !ok {
			return nil, fmt.Errorf("expecting name:tag for image target, got %s", value.Tag)
		}

		version := value.Version
		if version == "" {
			version = target.Tag()

			ref, err := reference.Parse(value.Default)
			if err == nil {
				if tagged, ok := ref.(reference.Tagged); ok {
					version = tagged.Tag()
				}
			}

		}

		customImages = append(customImages, CustomImage{
			Source:      value.Default,
			Target:      target,
			Version:     version,
			DefaultOnly: true,
		})
	}
	images := make([]reference.NamedTagged, 0, len(config.Images))
	for _, image := range config.Images {
		named, err := getNamedTagged(image)
		if err != nil {
			return nil, err
		}
		images = append(images, named)
	}

	var base reference.NamedTagged
	if config.Base != "" {
		var err error
		base, err = getNamedTagged(config.Base)
		if err != nil {
			return nil, err
		}
	}

	name := config.Name
	if name == "" {
		name = filepath.Base(path)
	}

	return &configurationSuite{
		config:       config,
		path:         path,
		base:         base,
		customImages: customImages,
		images:       images,

		resolvedName: name,
	}, nil
}

func getNamedTagged(image string) (reference.NamedTagged, error) {
	ref, err := reference.Parse(image)
	if err != nil {
		return nil, err
	}
	named, ok := ref.(reference.NamedTagged)
	if !ok {
		return nil, fmt.Errorf("Image reference must have name and tag: %s", image)
	}
	return named, nil
}

func parseSuites(suites []string) (map[string]*configurationSuite, error) {
	configs := map[string]*configurationSuite{}
	for _, suite := range suites {
		logrus.Debugf("Handling suite %s", suite)
		absPath, err := filepath.Abs(suite)
		if err != nil {
			return nil, fmt.Errorf("could not resolve %s: %s", suite, err)
		}

		info, err := os.Stat(absPath)
		if err != nil {
			return nil, fmt.Errorf("error statting %s: %s", suite, err)
		}
		if info.IsDir() {
			absPath = filepath.Join(absPath, "golem.conf")
			if _, err := os.Stat(absPath); err != nil {
				return nil, fmt.Errorf("error statting %s: %s", filepath.Join(suite, "golem.conf"), err)
			}
		}

		confBytes, err := ioutil.ReadFile(absPath)
		if err != nil {
			return nil, fmt.Errorf("unable to open configuration file %s: %s", absPath, err)
		}

		// Load
		var conf suitesConfiguration
		if err := toml.Unmarshal(confBytes, &conf); err != nil {
			return nil, fmt.Errorf("error unmarshalling %s: %s", absPath, err)
		}

		logrus.Debugf("Found %d test suites in %s", len(conf.Suites), suite)
		for _, sc := range conf.Suites {
			p := filepath.Dir(absPath)
			suiteConfig, err := newSuiteConfiguration(p, sc)
			if err != nil {
				return nil, err
			}

			name := suiteConfig.Name()
			_, ok := configs[name]
			for i := 1; ok; i++ {
				name = fmt.Sprintf("%s-%d", suiteConfig.Name(), i)
				_, ok = configs[name]
			}
			suiteConfig.SetName(name)
			configs[name] = suiteConfig
		}
	}

	return configs, nil
}

type customimageConfiguration struct {
	Tag     string `toml:"tag"`
	Default string `toml:"default"`
	Version string `toml:"version"`
}

type suitesConfiguration struct {
	Suites []suiteConfiguration `toml:"suite"`
}

type pretestConfiguration struct {
	Command string   `toml:"command"`
	Env     []string `toml:"env"`
}

type testRunConfiguration struct {
	Command string   `toml:"command"`
	Format  string   `toml:"format"`
	Env     []string `toml:"env"`
}

type suiteConfiguration struct {
	// Name is used to set the name of this suite, if none is set here then the name
	// should be set by the runner configuration or using the directory name
	Name string `toml:"name"`

	// Dind (or "Docker in Docker") used to determine whether a docker daemon will be run
	// inside the test container
	Dind bool `toml:"dind"`

	// Base is the base image to build the test from
	Base string `toml:"baseimage"`

	// Pretest is the commands to run before the test starts
	Pretest []pretestConfiguration `toml:"pretest"`

	// Runner are the commands to run for the test. Each command
	// must run without error for the suite to be considered passed.
	// Each command may have a different output format.
	Runner []testRunConfiguration `toml:"testrunner"`

	// Images which should exist in the test container
	// automatically set dind to true
	Images []string `toml:"images"`

	// CustomImages allow runtime selection of an image inside the container
	// automatically set dind to true
	CustomImages []customimageConfiguration `toml:"customimage"`
}

func assertTagged(image string) reference.NamedTagged {
	ref, err := reference.Parse(image)
	if err != nil {
		logrus.Panicf("Invalid reference %q: %v", image, err)
	}
	named, ok := ref.(reference.NamedTagged)
	if !ok {
		logrus.Panicf("Image reference must have name and tag: %s", image)
	}

	return named
}
