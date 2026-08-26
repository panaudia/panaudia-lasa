module github.com/panaudia/panaudia-lasa/server

go 1.26.0

require (
	github.com/joho/godotenv v1.5.1
	github.com/panaudia/lasa v0.1.2
	github.com/panaudia/moqtransport v0.8.5-lasa.2
	github.com/panaudia/panaudia-lasa/engine v0.0.0
	github.com/quic-go/quic-go v0.61.0
	github.com/quic-go/webtransport-go v0.12.0
	gopkg.in/hraban/opus.v2 v2.0.0-20230925203106-0188a62cb302
)

require (
	github.com/deckarep/golang-set/v2 v2.9.0 // indirect
	github.com/dunglas/httpsfv v1.1.0 // indirect
	github.com/golang-jwt/jwt/v5 v5.3.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/mengelbart/qlog v0.1.0 // indirect
	github.com/quic-go/qpack v0.6.0 // indirect
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.2 // indirect
	go.mongodb.org/mongo-driver v1.17.4 // indirect
	golang.org/x/crypto v0.54.0 // indirect
	golang.org/x/net v0.56.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.40.0 // indirect
	gonum.org/v1/gonum v0.17.0 // indirect
)

replace github.com/panaudia/panaudia-lasa/engine => ../engine

replace github.com/quic-go/webtransport-go => github.com/panaudia/webtransport-go v0.12.1-0.20260821153741-1bc7484acf3f
