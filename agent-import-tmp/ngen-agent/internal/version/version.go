package version

const (
	Name            = "ngen"
	Version         = "0.1.0"
	Protocol        = "ngen-stream-json"
	ProtocolVersion = 1
)

var (
	Commit  string
	BuiltAt string
)

type Info struct {
	Name            string `json:"name"`
	Version         string `json:"version"`
	Commit          string `json:"commit,omitempty"`
	BuiltAt         string `json:"built_at,omitempty"`
	Protocol        string `json:"protocol"`
	ProtocolVersion int    `json:"protocol_version"`
}

func Current() Info {
	return Info{
		Name:            Name,
		Version:         Version,
		Commit:          Commit,
		BuiltAt:         BuiltAt,
		Protocol:        Protocol,
		ProtocolVersion: ProtocolVersion,
	}
}
