module github.com/pashagolub/pglogwatch/cmd/pglogwatch

go 1.26.0

require (
	github.com/pashagolub/pglogwatch v0.0.0
	github.com/pashagolub/pglogwatch/compress v0.0.0
	github.com/stretchr/testify v1.12.1
)

require (
	github.com/klauspost/compress v1.19.2 // indirect
	github.com/ulikunitz/xz v0.5.16 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
)

replace github.com/pashagolub/pglogwatch => ../..

replace github.com/pashagolub/pglogwatch/compress => ../../compress
