package buildutil

import (
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/Sirupsen/logrus"
	"github.com/docker/distribution/digest"
	"github.com/docker/golem/versionutil"
)

var (
	// ErrCannotDownloadCommit is used when downloading is required but
	// a build has been specified by commit hash.
	ErrCannotDownloadCommit = errors.New("cannot download build by commit")
)

// BuildCache is a cache for storing specific versions of Docker
type BuildCache interface {
	// IsCached returns whether or not the version exist in the cache
	IsCached(versionutil.Version) bool

	// PutVersion puts the given file path in the cache using the
	// provided version for the cache.
	PutVersion(versionutil.Version, string) error

	// InstallVersion installs the provided version to the given
	// location. If the version cannot be retrieved an error will
	// be returned.
	InstallVersion(versionutil.Version, string) error
}

type fsBuildCache struct {
	root string
}

// NewFSBuildCache returns a build cache using the provided
// root directory as the cache storage.
func NewFSBuildCache(root string) BuildCache {
	return &fsBuildCache{
		root: root,
	}
}

func (bc *fsBuildCache) versionFile(v versionutil.Version) string {
	if v.Commit != "" {
		panic("cannot get release file with commit")
	}

	versionFile := filepath.Join(bc.root, fmt.Sprintf("%d.%d.%d", v.VersionNumber[0], v.VersionNumber[1], v.VersionNumber[2]))
	if v.Tag != "" {
		versionFile = versionFile + "-" + v.Tag
	}

	return versionFile
}

func (bc *fsBuildCache) getCached(v versionutil.Version) string {
	logrus.Debugf("Looking for cached version of %s", v)
	if v.Commit != "" {
		commitFile := filepath.Join(bc.root, v.Commit)
		if _, err := os.Stat(commitFile); err == nil {
			return commitFile
		}
		return ""
	}

	versionFile := bc.versionFile(v)
	if _, err := os.Stat(versionFile); err == nil {
		return versionFile
	}
	logrus.Debugf("Could not find version file at %s", versionFile)

	return ""
}

func initFile(f string) string {
	dir, name := filepath.Split(f)
	if strings.HasPrefix(name, "docker") {
		name = "dockerinit" + name[6:]
	} else {
		name = name + "-init"
	}
	return dir + name

}

func (bc *fsBuildCache) tempFile() (*os.File, error) {
	return ioutil.TempFile(bc.root, "tmp-")
}

func (bc *fsBuildCache) cleanupTempFile(tmp *os.File) error {
	if err := tmp.Close(); err != nil {
		log.Printf("Failed to close temp file %v: %s", tmp.Name(), err)
	}
	return os.Remove(tmp.Name())
}

func (bc *fsBuildCache) saveVersion(tmp *os.File, v versionutil.Version) (string, error) {
	source := tmp.Name()
	if err := tmp.Close(); err != nil {
		log.Printf("Failed to close temp file %v: %s", tmp.Name(), err)
	}
	// TODO: Ensure source version matches

	target := bc.versionFile(v)
	if err := os.Rename(source, target); err != nil {
		return "", err
	}
	return target, nil
}

func (bc *fsBuildCache) IsCached(v versionutil.Version) bool {
	return bc.getCached(v) != ""
}

func binaryDigest(source string) (digest.Digest, error) {
	f, err := os.Open(source)
	if err != nil {
		return "", err
	}
	defer f.Close()
	return digest.FromReader(f)
}

func (bc *fsBuildCache) PutVersion(v versionutil.Version, source string) error {
	cached := bc.getCached(v)
	if cached != "" {
		sourceDgst, err := binaryDigest(source)
		if err != nil {
			return err
		}
		cachedDgst, err := binaryDigest(cached)
		if err != nil {
			return err
		}
		if sourceDgst == cachedDgst {
			return nil
		}
		logrus.Debugf("Overwriting %s with %s", cached, source)
	} else if v.Commit != "" {
		cached = filepath.Join(bc.root, v.Commit)
	} else {
		cached = bc.versionFile(v)
	}
	if err := CopyFile(source, cached, 0755); err != nil {
		return err
	}
	sourceInit := initFile(source)
	if _, err := os.Stat(sourceInit); err == nil {
		cachedInit := initFile(cached)
		if err := CopyFile(sourceInit, cachedInit, 0755); err != nil {
			return err
		}
	}

	return nil
}

func (bc *fsBuildCache) InstallVersion(v versionutil.Version, target string) error {
	cached := bc.getCached(v)
	var cachedInit string
	if cached == "" {
		if v.Commit != "" {
			return ErrCannotDownloadCommit
		}
		resp, err := http.Get(v.DownloadURL())
		if err != nil {
			return err
		}

		tf, err := bc.tempFile()
		if err != nil {
			return err
		}

		_, err = io.Copy(tf, resp.Body)
		if err != nil {
			if err := bc.cleanupTempFile(tf); err != nil {
				// Just log
				log.Printf("Error cleaning up temp file %v: %s", tf.Name(), err)
			}
			return err
		}

		cached, err = bc.saveVersion(tf, v)
		if err != nil {
			return err
		}

		// Remove any "-init"
		cachedInit = initFile(cached)
		if _, err := os.Stat(cachedInit); err == nil {
			if err := os.Remove(cachedInit); err != nil {
				return err
			}
		}
	} else {
		cachedInit = initFile(cached)
	}

	if err := CopyFile(cached, target, 0755); err != nil {
		return err
	}

	targetInit := initFile(target)
	if _, err := os.Stat(cachedInit); err == nil {
		// Create target file, check if name starts with docker, replace with dockerinit
		return CopyFile(cachedInit, targetInit, 0755)
	}

	if _, err := os.Stat(targetInit); err == nil {
		// Truncate file, do not remove since operator may only have access
		// to file and not directory. Future calls may rely on overwriting
		// the content of this file.
		vf, err := os.OpenFile(targetInit, os.O_TRUNC|os.O_WRONLY, 0755)
		if err != nil {
			return err
		}
		return vf.Close()
	}

	return nil
}
