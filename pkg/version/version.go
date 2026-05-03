// Package version provides application version information
// set at build time for the controller and control-plane.
package version

import "strings"

var (
	Version             string
	ControlPlaneVersion string
	ProjectVersion      string
)

func GetVersion() string {
	return Version
}

// SetProjectVersion installs a fallback project version (typically the embedded
// VERSION file from main.go's init) so dev/local builds report a real semver
// even without -ldflags injection. ldflags-set values take precedence.
func SetProjectVersion(v string) {
	if ProjectVersion != "" {
		return
	}
	ProjectVersion = strings.TrimSpace(v)
}

// GetProjectVersion returns the elchi-backend project version.
// Resolution: build-time ldflags > main.go embed (SetProjectVersion) > "dev".
func GetProjectVersion() string {
	if ProjectVersion == "" {
		return "dev"
	}
	return ProjectVersion
}
