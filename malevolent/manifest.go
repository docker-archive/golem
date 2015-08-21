package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"

	"github.com/Sirupsen/logrus"
	"github.com/docker/distribution/digest"
	"github.com/docker/distribution/manifest"
)

type manifestChanger struct {
	http.Handler
}

// alterManifest changes the outbound manifest by adding a key. This should
// cause signature verification to fail.
func (m manifestChanger) alterManifest(rw http.ResponseWriter, r *http.Request) {
	recorder := httptest.NewRecorder()

	m.Handler.ServeHTTP(recorder, r)

	b := recorder.Body.Bytes()
	indent := detectJSONIndent(b)
	key := "malevolent"
	value := "added by malevolent proxy"
	var addition []byte
	if indent == "" {
		addition = []byte(fmt.Sprintf("%q:%q", key, value))
	} else {
		addition = []byte(fmt.Sprintf("\n%s%q: %q,", indent, key, value))
	}

	copied := make([]byte, len(b)+len(addition), len(b)+len(addition))
	copy(copied[0:1], b[0:1])
	copy(copied[1:len(addition)+1], addition)
	copy(copied[len(addition)+1:], b[1:])

	recorder.Header().Set("Content-Length", strconv.Itoa(len(copied)))
	copyHeader(rw.Header(), recorder.Header())
	rw.WriteHeader(recorder.Code)

	n, err := rw.Write(copied)
	if err != nil {
		logrus.Errorf("Error writing: %s", err)
		return
	}
	if n != len(copied) {
		logrus.Errorf("Short write: wrote %d, expected %d", n, len(copied))
	}
}

// rename changes the name in a manifest and re-signs with a different key
func (m manifestChanger) rename(rw http.ResponseWriter, r *http.Request, newName string) {
	recorder := httptest.NewRecorder()

	m.Handler.ServeHTTP(recorder, r)

	b := recorder.Body.Bytes()

	var sm manifest.SignedManifest
	if err := json.Unmarshal(b, &sm); err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}

	sm.Manifest.Name = newName

	newSm, err := manifest.Sign(&sm.Manifest, key)
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}

	if recorder.Header().Get("Docker-Content-Digest") != "" {
		payload, err := newSm.Payload()
		if err != nil {
			http.Error(rw, err.Error(), http.StatusInternalServerError)
			return
		}
		dgst, err := digest.FromBytes(payload)
		if err != nil {
			http.Error(rw, err.Error(), http.StatusInternalServerError)
			return
		}

		recorder.Header().Set("Docker-Content-Digest", dgst.String())
	}

	copied, err := json.MarshalIndent(newSm, "", "   ")
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}

	recorder.Header().Set("Content-Length", strconv.Itoa(len(copied)))
	copyHeader(rw.Header(), recorder.Header())
	rw.WriteHeader(recorder.Code)

	n, err := rw.Write(copied)
	if err != nil {
		logrus.Errorf("Error writing: %s", err)
		return
	}
	if n != len(copied) {
		logrus.Errorf("Short write: wrote %d, expected %d", n, len(copied))
	}
}

// badRemoteDigest
// stripSignature

// changeSignature

// addSignature

func (m manifestChanger) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		m.Handler.ServeHTTP(rw, r)
		return
	}

	operation := extractOperation(r)
	switch operation {
	case "rename":
		m.rename(rw, r, "newname")
	case "badsignature":
		m.alterManifest(rw, r)
	default:
		logrus.Infof("No manifest operation for %q, passing through", operation)
		m.Handler.ServeHTTP(rw, r)
	}
}

func detectJSONIndent(jsonContent []byte) (indent string) {
	if len(jsonContent) > 2 && jsonContent[0] == '{' && jsonContent[1] == '\n' {
		quoteIndex := bytes.IndexRune(jsonContent[1:], '"')
		if quoteIndex > 0 {
			indent = string(jsonContent[2 : quoteIndex+1])
		}
	}
	return
}
