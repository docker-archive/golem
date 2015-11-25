package versionutil

// DownloadURL returns the download URL for the
// operating system and architecture for the system
// being built for.
func (v Version) DownloadURL() string {
	return v.downloadURL("Linux", "x86_64")
}
