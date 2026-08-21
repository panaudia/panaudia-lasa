package common

import (
	"encoding/json"
	"math"

	"github.com/google/uuid"
)

const DIAGONAL = "ZPbjODLQX2IjNlRN6cNTdnbY"
const SLOPE = "feQc89zswulLpQt9UuOx6nbdRLC4z16bqOD="

const FRAME_SIZE = 240
const SAMPLE_RATE = 48000
const INPUT_FRAME_SIZE = 960

const READ_TIMEOUT_SECONDS = 3

const REVERB_CHANNELS = 4

const DISABLE_IN = false
const DISABLE_RENDER = false
const DISABLE_OUT = false

func ChannelCountForOrder(order int) int {
	return (order + 1) * (order + 1)
}

func OrderForChannelCount(channelCount int) int {
	return int(math.Sqrt(float64(channelCount)) - 1)
}

type MixerConfig struct {
	MaxNodes     int
	FrameSize    int
	ChannelCount int
	Order        int
	Size         float64
	ReverbPreset int
}

type Position struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	Z float64 `json:"z"`
}

type Position32 struct {
	X float32 `json:"x"`
	Y float32 `json:"y"`
	Z float32 `json:"z"`
}

type Rotation struct {
	Yaw   float64 `json:"yaw"`
	Pitch float64 `json:"pitch"`
	Roll  float64 `json:"roll"`
}

type Location struct {
	Position Position `json:"position"`
	Rotation Rotation `json:"rotation"`
}

type PolarPosition struct {
	Azimuth   float64
	Elevation float64
	Distance  float64
}

type PolarPosition32 struct {
	Azimuth   float32
	Elevation float32
	Distance  float32
}

type Index struct {
	X int
	Y int
	Z int
}

type HostNPort struct {
	Host string
	Port int
}

type NodeInfo struct {
	Uuid     string   `json:"i"`
	Name     string   `json:"n"`
	Position Position `json:"p"`
	Rotation Rotation `json:"r"`
	Volume   float64  `json:"v"`
	Colour   string   `json:"c"`
}

type MultiTrackPositionInfo struct {
	Track    uint32   `json:"track"`
	Position Position `json:"position"`
	Rotation Rotation `json:"rotation"`
}

type NodeInfo2 struct {
	I uuid.UUID `json:"i"`
	X float32   `json:"x"`
	Y float32   `json:"y"`
	Z float32   `json:"z"`
	W float32   `json:"w"`
	P float32   `json:"p"`
	R float32   `json:"r"`
	V float32   `json:"v"`
	G int32     `json:"g"`
}

type NodeInfo3 struct {
	Uuid     uuid.UUID
	Position Position
	Rotation Rotation
	Volume   float64
	Gone     int32
}

func NodeInfoToString(node NodeInfo) string {
	if buffer, err := json.Marshal(node); err != nil {
		panic(err)
	} else {
		return string(buffer)
	}
}

func NodeInfoFromString(jsonString string) NodeInfo {
	node := NodeInfo{}
	if err := json.Unmarshal([]byte(jsonString), &node); err != nil {
		panic(err)
	}
	return node
}

func NodeInfo2ToString(node NodeInfo2) string {
	if buffer, err := json.Marshal(node); err != nil {
		panic(err)
	} else {
		return string(buffer)
	}
}

func NodeInfo2FromString(jsonString string) NodeInfo2 {
	node := NodeInfo2{}
	if err := json.Unmarshal([]byte(jsonString), &node); err != nil {
		panic(err)
	}
	return node
}

type J map[string]interface{}

type UdpNodeIODelegate interface {
	GetTick() int64
}

const (
	REVERB_NONE        = iota
	REVERB_TIGHT_ROOM  = iota
	REVERB_SMALL_ROOM  = iota
	REVERB_MEDIUM_ROOM = iota
	REVERB_LARGE_HALL  = iota
	REVERB_CATHEDRAL   = iota
)
