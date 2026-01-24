// Package version provides application version information
// set at build time for the controller and control-plane.
package version

var (
	Version             string
	ControlPlaneVersion string
)

func GetVersion() string {
	return Version
}
