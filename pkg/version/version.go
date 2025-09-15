package version

var (
	Version             string
	ControlPlaneVersion string
)

func GetVersion() string {
	return Version
}

func GetControlPlaneVersion() string {
	return ControlPlaneVersion
}
