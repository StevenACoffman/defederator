module github.com/StevenACoffman/defederator

go 1.26.2

require (
	github.com/99designs/gqlgen v0.17.73
	github.com/Khan/genqlient v0.8.1
	github.com/StevenACoffman/gorouter v0.0.0-00010101000000-000000000000
	github.com/bmatcuk/doublestar/v4 v4.10.0
	github.com/goccy/go-yaml v1.17.1
	github.com/gqlgo/gqlgenc v0.0.0-00010101000000-000000000000
	github.com/vektah/gqlparser/v2 v2.5.33
)

require (
	github.com/agnivade/levenshtein v1.2.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/sosodev/duration v1.3.1 // indirect
	golang.org/x/mod v0.33.0 // indirect
	golang.org/x/sync v0.19.0 // indirect
	golang.org/x/text v0.24.0 // indirect
	golang.org/x/tools v0.42.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace (
	github.com/StevenACoffman/gorouter => ../gorouter
	github.com/gqlgo/gqlgenc => ../gqlgenc
)
