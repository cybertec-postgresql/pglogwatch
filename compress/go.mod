module github.com/cybertec-postgresql/pglogwatch/compress

go 1.26.0

require (
	github.com/klauspost/compress v1.19.2
	github.com/ulikunitz/xz v0.5.16
)

require (
	github.com/cybertec-postgresql/pglogwatch v0.0.0
	github.com/stretchr/testify v1.12.1
	go.yaml.in/yaml/v3 v3.0.5 // indirect
)

replace github.com/cybertec-postgresql/pglogwatch => ..
