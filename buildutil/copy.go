package buildutil

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// CopyFile copies the source file into the destination file
func CopyFile(source, dest string, mode os.FileMode) error {
	if _, err := os.Stat(source); os.IsNotExist(err) {
		return fmt.Errorf("source file not found at %q", source)
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return fmt.Errorf("error creating directory for %q: %s", dest, err)
	}

	vf, err := os.OpenFile(dest, os.O_TRUNC|os.O_CREATE|os.O_WRONLY, mode)
	if err != nil || vf == nil {
		return fmt.Errorf("error opening target file %q: %s", dest, err)
	}
	defer vf.Close()

	bv, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("error opening source file %q: %s", source, err)
	}
	defer bv.Close()

	_, err = io.Copy(vf, bv)
	if err != nil {
		return fmt.Errorf("error copying file: %s", err)
	}

	return nil
}
