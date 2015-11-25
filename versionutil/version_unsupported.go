// +build !linux

package versionutil

func (v Version) DownloadURL() string {
	panic("cannot get download URL")
}
