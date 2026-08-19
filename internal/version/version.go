package version

// Version is the semantic version, overridden at link time with
// -X github.com/misakayyds/lantally/internal/version.Version=v0.1.0
var (
	Version = "0.1.0-dev"
	Commit  = "unknown"
	Date    = "unknown"
)

func String() string {
	if Commit == "" || Commit == "unknown" {
		return Version
	}
	return Version + " (" + Commit + ")"
}
