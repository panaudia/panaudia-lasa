package common

import (
	"errors"
	"slices"
	"strconv"

	"github.com/google/uuid"
)

type RocPorts struct {
	Host    string `json:"host"`
	Source  uint32 `json:"source"`
	Repair  uint32 `json:"repair"`
	Control uint32 `json:"control"`
}

type AmbisonicConfig struct {
	Channels      uint32 `json:"channels"`
	Normalisation string `json:"normalisation"`
}

type TicketConfig struct {
	Attenuation float64     `json:"attenuation"`
	Gain        float64     `json:"gain"`
	Priority    bool        `json:"priority"`
	SubSpaces   []uuid.UUID `json:"subspaces"`
	Roles       []string    `json:"roles"`
	Attrs       J           `json:"attrs"`
	Name        string      `json:"name"`
	Uuid        string      `json:"uuid"`
	BouncerHost string      `json:"bouncer"`
	MixerHost   string      `json:"mixer"`
}

type RocInConnectConfig struct {
	BouncerHost string `json:"bouncer"`
	MixerHost   string `json:"mixer"`
	Channels    int    `json:"channels"`
}

type DirectTicketConfig struct {
	Name        string   `json:"name"`
	Uuid        string   `json:"uuid"`
	Attenuation float64  `json:"attenuation"`
	Gain        float64  `json:"gain"`
	Priority    bool     `json:"priority"`
	Roles       []string `json:"roles"`
	Attrs       J        `json:"attrs"`
}

type NodeConfig struct {
	Name        string    `json:"name"`
	Uuid        uuid.UUID `json:"uuid"`
	Attrs       J         `json:"attrs"`
	Position    Position  `json:"position"`
	Rotation    Rotation  `json:"rotation"`
	BouncerHost string    `json:"bouncer"`
	MixerHost   string    `json:"mixer"`
	ReturnData  bool      `json:"data"`
	// Roles the ticket holder claims, used for command authorisation.
	// Loaded from the `roles` field of the JWT's `panaudia` claim. An
	// empty list means the holder cannot invoke any command.
	Roles []string `json:"roles"`
	// ReadCaps is the resolved union of read scopes granted by the
	// holder's roles, computed at authentication time by the
	// Authoriser. Keys are constants from core/commands/spec.go (e.g.
	// commands.ReadCapEntityAll). nil/empty means default visibility:
	// per-client filter on the entity stream, subspace-overlap filter
	// on the attributes stream. Note: even with entity.all granted,
	// the cache backfill ordering invariant (entity-before-attributes)
	// still holds — see plan/history/distributed-state-sync/topic-ordering.md.
	ReadCaps map[string]bool `json:"-"`
	SpaceNodeConfig
}

type RocInputConfig struct {
	Nodes []RocInputNodeConfig `json:"nodes"`
}

type RocOutputConfig struct {
	Channels      int        `json:"channels"`
	Normalisation string     `json:"normalisation"`
	Node          NodeConfig `json:"node"`
	Ports         RocPorts   `json:"ports"`
}

type RocInputNodeConfig struct {
	Name        string   `json:"name"`
	Uuid        string   `json:"uuid"`
	Subspaces   []string `json:"subspaces"`
	Attrs       J        `json:"attrs"`
	Position    Position `json:"position"`
	Rotation    Rotation `json:"rotation"`
	Gain        float64  `json:"gain"`
	Attenuation float64  `json:"attenuation"`
}

type RocConfig struct {
	Nodes []NodeConfig `json:"nodes"`
}

type SpaceNodeConfig struct {
	Gain        float64 `json:"gain"`
	Attenuation float64 `json:"attenuation"`
	Input       bool    `json:"input"`
	Priority    bool    `json:"priority"`
	Tone        float64 `json:"tone"`
	NullOut     bool    `json:"nullout"`
	// Raw marks a raw float INPUT codec on the wire (ROC in) — an ingress
	// concern, distinct from RawOutput below.
	Raw bool `json:"raw"`
	// RawOutput marks a raw-ambisonic OUTPUT consumer (ROC out): its render
	// mode is CLASSIC — single world-frame mix, no encode-side rotation,
	// ITD, parallax or NFC — regardless of PANAUDIA_BILATERAL (plan design
	// decision 10: render mode is a per-output property; the raw-output
	// contract is standard world-frame ambisonics, permanently).
	RawOutput bool `json:"raw_output"`
	SubSpaces     []uuid.UUID `json:"subspaces"`
	InputChannels int         `json:"input_channels"` // 1=mono (MOQ), 2=stereo (WebRTC); 0 defaults to 2
	ResumeOpID    uint64      `json:"-"`              // cache resume point; 0 = full backfill
}

func DefaultNodeConfig() NodeConfig {
	config := NodeConfig{}
	config.Gain = 1.0
	config.Attenuation = 2.0
	config.Input = true
	config.Priority = false
	config.Tone = 0.0
	config.NullOut = false
	config.Raw = false
	return config
}

func RocConfigFromRocInputConfig(inputConfig RocInputConfig, mixerHost string, bouncerHost string) (RocConfig, error) {

	roc := RocConfig{Nodes: make([]NodeConfig, 0)}

	for _, inputNodeConfig := range inputConfig.Nodes {
		nodeConfig, err := NodeConfigFromRocInputNodeConfig(inputNodeConfig, mixerHost, bouncerHost)
		if err != nil {
			return RocConfig{}, err
		}
		roc.Nodes = append(roc.Nodes, nodeConfig)
	}

	return roc, nil
}

func NodeConfigFromRocInputNodeConfig(input RocInputNodeConfig, mixerHost string, bouncerHost string) (NodeConfig, error) {

	id, err := uuid.Parse(input.Uuid)

	if err != nil {
		err2 := errors.New("Invalid value for uuid")
		return NodeConfig{}, err2
	}

	subspaces := make([]uuid.UUID, 0)

	if input.Subspaces != nil {

		for _, subspace := range input.Subspaces {
			subspace, err := uuid.Parse(subspace)
			if err != nil {
				err2 := errors.New("Invalid value for subspace")
				return NodeConfig{}, err2
			}
			subspaces = append(subspaces, subspace)
		}
	}

	config := NodeConfig{}
	config.Name = input.Name
	config.Uuid = id
	config.SubSpaces = subspaces
	config.Attrs = J{"connection": input.Attrs}
	config.Position = input.Position
	config.Rotation = input.Rotation
	config.ReturnData = false
	config.MixerHost = mixerHost
	config.BouncerHost = bouncerHost
	config.Gain = input.Gain
	config.Attenuation = input.Attenuation
	config.Input = true
	config.Priority = true
	config.Tone = 0.0
	config.NullOut = false
	config.Raw = true

	return config, nil
}

func NodeConfigFromDirectTicket(ticketConfig DirectTicketConfig) (NodeConfig, error) {

	id, err := uuid.Parse(ticketConfig.Uuid)
	if err != nil {
		return NodeConfig{}, err
	}

	nodeConfig := DefaultNodeConfig()
	nodeConfig.Uuid = id

	if ticketConfig.Name == "" {
		nodeConfig.Name = ticketConfig.Uuid
	} else {
		nodeConfig.Name = ticketConfig.Name
	}

	if ticketConfig.Attenuation > 3.0 || ticketConfig.Attenuation < 0.0 {
		return NodeConfig{}, errors.New("Invalid value for attenuation")
	} else if ticketConfig.Attenuation > 0.0 {
		nodeConfig.Attenuation = ticketConfig.Attenuation
	}

	if ticketConfig.Gain > 3.0 || ticketConfig.Gain < 0.0 {
		return NodeConfig{}, errors.New("Invalid value for gain")
	} else if ticketConfig.Gain > 0.0 {
		nodeConfig.Gain = ticketConfig.Gain
	}

	if ticketConfig.Priority {
		nodeConfig.Priority = true
	}

	nodeConfig.Roles = ticketConfig.Roles
	nodeConfig.Attrs = J{"ticket": ticketConfig.Attrs}

	return nodeConfig, nil
}

func NodeConfigFromTicket(ticketConfig TicketConfig) (NodeConfig, error) {

	id, err := uuid.Parse(ticketConfig.Uuid)
	if err != nil {
		return NodeConfig{}, err
	}

	nodeConfig := DefaultNodeConfig()
	nodeConfig.Uuid = id
	nodeConfig.BouncerHost = ticketConfig.BouncerHost
	nodeConfig.MixerHost = ticketConfig.MixerHost

	if ticketConfig.Name == "" {
		nodeConfig.Name = ticketConfig.Uuid
	} else {
		nodeConfig.Name = ticketConfig.Name
	}

	if ticketConfig.Attenuation > 3.0 || ticketConfig.Attenuation < 0.0 {
		return NodeConfig{}, errors.New("Invalid value for attenuation")
	} else {
		nodeConfig.Attenuation = ticketConfig.Attenuation
	}

	if ticketConfig.Gain > 3.0 || ticketConfig.Gain < 0.0 {
		return NodeConfig{}, errors.New("Invalid value for gain")
	} else {
		nodeConfig.Gain = ticketConfig.Gain
	}

	if ticketConfig.Priority {
		nodeConfig.Priority = true
	}

	nodeConfig.SubSpaces = ticketConfig.SubSpaces
	nodeConfig.Roles = ticketConfig.Roles

	nodeConfig.Attrs = J{"ticket": ticketConfig.Attrs}

	return nodeConfig, nil
}

func float64FromQuery(name string, queryValues map[string][]string) (float64, error) {
	values := queryValues[name]
	if len(values) == 0 {
		return 0, errors.New("no such value")
	}
	value, err := strconv.ParseFloat(values[0], 64)
	if err != nil {
		return 0, err
	}
	return value, nil
}

// SubspacesFromQuery parses repeated `subspaces` query params into a UUID
// slice (mirrors the per-node `subspaces` list carried on the ROC input
// path). The param is list-capable — one `subspaces=<uuid>` item per
// subspace — though clients currently send at most one. An invalid UUID is
// an error; an absent param yields an empty (non-nil) slice.
func SubspacesFromQuery(queryValues map[string][]string) ([]uuid.UUID, error) {
	subspaces := make([]uuid.UUID, 0)
	for _, raw := range queryValues["subspaces"] {
		id, err := uuid.Parse(raw)
		if err != nil {
			return nil, errors.New("Invalid value for subspace")
		}
		subspaces = append(subspaces, id)
	}
	return subspaces, nil
}

func Uint32FromQuery(name string, queryValues map[string][]string) (uint32, error) {
	values := queryValues[name]
	if len(values) == 0 {
		return 0, errors.New("no such value")
	}
	value, err := strconv.ParseInt(values[0], 10, 64)
	if err != nil {
		return 0, err
	}
	return uint32(value), nil
}

func Uint64FromQuery(name string, queryValues map[string][]string) (uint64, error) {
	values := queryValues[name]
	if len(values) == 0 {
		return 0, errors.New("no such value")
	}
	value, err := strconv.ParseUint(values[0], 10, 64)
	if err != nil {
		return 0, err
	}
	return value, nil
}

func AddQueryAttrs(queryValues map[string][]string, config NodeConfig) NodeConfig {
	config.Position, config.Rotation, config.ReturnData = PoseFromFromQuery(queryValues)
	config.Attrs["connection"] = UserAttrsFromQuery(queryValues)
	if v, err := Uint64FromQuery("resumeOpId", queryValues); err == nil {
		config.ResumeOpID = v
	}
	return config
}

func PoseFromFromQuery(queryValues map[string][]string) (Position, Rotation, bool) {

	names := []string{"x", "y", "z", "yaw", "pitch", "roll"}
	values := make(map[string]float64)

	dataValues := queryValues["presence"]
	var returnData bool
	if len(dataValues) == 0 {
		returnData = false
	} else {
		returnData = dataValues[0] == "true"
	}

	for _, name := range names {
		value, err := float64FromQuery(name, queryValues)
		if err != nil {
			return Position{X: 0.5, Y: 0.5, Z: 0.5}, Rotation{}, returnData
		} else {
			values[name] = value
		}
	}

	return Position{X: values["x"], Y: values["y"], Z: values["z"]},
		Rotation{Yaw: values["yaw"], Pitch: values["pitch"], Roll: values["roll"]},
		returnData
}

func PositionFromFromQuery(queryValues map[string][]string) Position {

	names := []string{"x", "y", "z"}
	values := make(map[string]float64)

	for _, name := range names {
		value, err := float64FromQuery(name, queryValues)
		if err != nil {
			return Position{X: 0.5, Y: 0.5, Z: 0.5}
		} else {
			values[name] = value
		}
	}

	return Position{X: values["x"], Y: values["y"], Z: values["z"]}
}

func UserAttrsFromQuery(queryValues map[string][]string) J {

	attributes := J{}
	reserved_names := []string{"ticket", "x", "y", "z", "yaw", "pitch", "roll", "presence", "resumeOpId"}

	for k, v := range queryValues {
		if !slices.Contains(reserved_names, k) {
			attributes[k] = v[0]
		}
	}
	return attributes
}

type APIError struct {
	ErrorMsg string `json:"error"`
}

func NodeConfigFromQuery(queryValues map[string][]string) (NodeConfig, error) {

	nodeConfig := DefaultNodeConfig()

	uuids := queryValues["uuid"]
	if len(uuids) == 0 {
		nodeConfig.Uuid = uuid.New()
	} else {
		id, err := uuid.Parse(uuids[0])
		if err != nil {
			return NodeConfig{}, err
		}
		nodeConfig.Uuid = id
	}

	names := queryValues["name"]
	if len(names) == 0 {
		nodeConfig.Name = nodeConfig.Uuid.String()
	} else {
		nodeConfig.Name = names[0]
	}

	attenuations := queryValues["attenuation"]
	if len(attenuations) == 0 {
	} else {
		attenuation, err := strconv.ParseFloat(attenuations[0], 64)
		if err != nil {
			return NodeConfig{}, errors.New("Invalid value for attenuation")
		}
		if attenuation > 3.0 || attenuation < 0.0 {
			return NodeConfig{}, errors.New("Invalid value for attenuation")
		}
		nodeConfig.Attenuation = attenuation
	}

	gains := queryValues["gain"]
	if len(gains) == 0 {
	} else {
		gain, err := strconv.ParseFloat(gains[0], 64)
		if err != nil {
			return NodeConfig{}, errors.New("Invalid value for gain")
		}
		if gain > 3.0 || gain < 0.0 {
			return NodeConfig{}, errors.New("Invalid value for gain")
		}
		nodeConfig.Gain = gain
	}

	prioritys := queryValues["priority"]
	if len(prioritys) == 0 {
	} else {
		if prioritys[0] == "true" {
			nodeConfig.Priority = true
		}
	}

	nodeConfig.Attrs = J{}
	return nodeConfig, nil
}
