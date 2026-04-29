// Package version provides application version information
// set at build time for the controller and control-plane.
package version

var (
	Version             string
	ControlPlaneVersion string
	ProjectVersion      string
)

func GetVersion() string {
	return Version
}

// GetProjectVersion returns the elchi-backend project version, injected at
// build time from the VERSION file via ldflags. Falls back to "dev" for
// non-release builds (e.g. local `go run`).
func GetProjectVersion() string {
	if ProjectVersion == "" {
		return "dev"
	}
	return ProjectVersion
}
