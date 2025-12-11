package version

var (
	Version             string
	ControlPlaneVersion string
)

func GetVersion() string {
	return Version
}
