module github.com/cybertec-postgresql/pglogwatch/cmd/pglogwatch

go 1.24

require (
	github.com/cybertec-postgresql/pglogwatch v0.0.0
	github.com/cybertec-postgresql/pglogwatch/compress v0.0.0
)

require (
	github.com/klauspost/compress v1.19.2 // indirect
	github.com/ulikunitz/xz v0.5.16 // indirect
)

replace github.com/cybertec-postgresql/pglogwatch => ../..

replace github.com/cybertec-postgresql/pglogwatch/compress => ../../compress
