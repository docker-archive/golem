package main

import (
	"archive/tar"
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"

	"github.com/Sirupsen/logrus"
	"github.com/docker/docker/pkg/archive"
)

type blobChanger struct {
	http.Handler
}

func tarCopy(w *tar.Writer, r *tar.Reader) error {
	for {
		hdr, err := r.Next()
		if err == io.EOF {
			// end of tar archive
			return nil
		}
		if err != nil {
			return err
		}
		if err := w.WriteHeader(hdr); err != nil {
			return err
		}
		if _, err := io.Copy(w, r); err != nil {
			return err
		}
	}
}

func addFile(w *tar.Writer, name string, contents []byte) error {
	// Use similar file info to /etc/hosts
	fi, err := os.Stat("/etc/hosts")
	if err != nil {
		return err
	}
	h, err := tar.FileInfoHeader(fi, "")
	if err != nil {
		return err
	}
	h.Name = name
	h.Size = int64(len(contents))
	if err := w.WriteHeader(h); err != nil {
		return err
	}
	if _, err := w.Write(contents); err != nil {
		return err
	}
	return nil
}

type writeCloser struct {
	io.Writer
}

func (writeCloser) Close() error {
	return nil
}

func (b blobChanger) addFile(rw http.ResponseWriter, r *http.Request) {
	recorder := httptest.NewRecorder()

	b.Handler.ServeHTTP(recorder, r)

	inflated, err := archive.DecompressStream(bytes.NewReader(recorder.Body.Bytes()))
	if err != nil {
		logrus.Errorf("Error decompressing: %s", err)
		http.Error(rw, "Error handling tar stream in proxy", 500)
		return
	}

	copied := bytes.NewBuffer(nil)
	deflater, err := archive.CompressStream(writeCloser{copied}, archive.Gzip)
	if err != nil {
		logrus.Errorf("Error compressing: %s", err)
		http.Error(rw, "Error handling tar stream in proxy", 500)
		return
	}

	tw := tar.NewWriter(deflater)
	if err := addFile(tw, "/etc/malicious.txt", []byte("#Bad bad stuff")); err != nil {
		logrus.Errorf("Error adding file: %s", err)
		http.Error(rw, "Error handling tar stream in proxy", 500)
		return
	}

	if err := tarCopy(tw, tar.NewReader(inflated)); err != nil {
		logrus.Errorf("Error copying: %s", err)
		http.Error(rw, "Error handling tar stream in proxy", 500)
		return
	}

	recorder.Header().Set("Content-Length", strconv.Itoa(len(copied.Bytes())))
	copyHeader(rw.Header(), recorder.Header())
	rw.WriteHeader(recorder.Code)

	n, err := rw.Write(copied.Bytes())
	if err != nil {
		logrus.Errorf("Error writing: %s", err)
		return
	}
	if n != copied.Len() {
		logrus.Errorf("Short write: wrote %d, expected %d", n, copied.Len())
	}
}

func (b blobChanger) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		b.Handler.ServeHTTP(rw, r)
		return
	}

	operation := extractOperation(r)
	switch operation {
	case "addfile":
		b.addFile(rw, r)
	default:
		logrus.Infof("No blob operation for %q, passing through", operation)
		b.Handler.ServeHTTP(rw, r)
	}
}
