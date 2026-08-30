module github.com/cybertec-postgresql/pglogwatch/cmd/pglogwatch

go 1.24

require (
	github.com/cybertec-postgresql/pglogwatch v0.0.0
	github.com/cybertec-postgresql/pglogwatch/compress v0.0.0
	github.com/stretchr/testify v1.12.1
)

require (
	github.com/klauspost/compress v1.19.2 // indirect
	github.com/ulikunitz/xz v0.5.16 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
)

replace github.com/cybertec-postgresql/pglogwatch => ../..

replace github.com/cybertec-postgresql/pglogwatch/compress => ../../compress
