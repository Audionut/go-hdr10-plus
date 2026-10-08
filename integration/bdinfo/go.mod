module github.com/Audionut/go-hdr10-plus/integration/bdinfo

go 1.27.0

require (
	github.com/Audionut/go-hdr10-plus v0.0.0
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

// Development-only: issue-45 APIs are not yet available at an immutable release.
replace github.com/Audionut/go-hdr10-plus => ../..

replace github.com/autobrr/go-bdinfo => ../../../go-bdinfo
