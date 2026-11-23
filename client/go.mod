module cvms/client

go 1.26.0

require (
	cvms/shared v0.0.0
	github.com/go-chi/chi/v5 v5.5.0
	github.com/gorilla/websocket v1.5.3
	golang.org/x/crypto v0.59.0
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/kardianos/service v1.3.0 // indirect
	golang.org/x/sys v0.50.0 // indirect
)

replace cvms/shared => ../shared
