module github.com/Audionut/go-hdr10-plus/integration/bdinfo

go 1.27.0

require (
	github.com/Audionut/go-hdr10-plus v0.0.0-20261012140645-34d94903fa8c
	github.com/autobrr/go-bdinfo v0.0.0
)

require (
	github.com/abema/go-mp4 v1.7.3 // indirect
	github.com/asticode/go-astikit v0.30.0 // indirect
	github.com/asticode/go-astits v1.16.0 // indirect
	github.com/at-wat/ebml-go v0.19.4 // indirect
	github.com/google/uuid v1.1.2 // indirect
	golang.org/x/image v0.46.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/text v0.42.0 // indirect
)

// The collector API is not yet merged upstream; use its immutable development head.
replace github.com/autobrr/go-bdinfo => github.com/Audionut/go-bdinfo v0.2.1-0.20261019003536-42be7cb0c64c
