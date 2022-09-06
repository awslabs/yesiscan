.PHONY: all build release

# These values get pulled in from these magic dot files to make builds that
# already contain these values. They set these config values automatically.
AUTO := $(shell cat ../../.autoConfigURI 2>/dev/null || echo '')
COOKIE := $(shell cat ../../.autoConfigCookiePath 2>/dev/null || echo '')

all: build

build:
	#@go build && echo "built binary to: $(PWD)/yesiscan"
	@go build -ldflags="-X main.autoConfigURI=$(AUTO) -X main.autoConfigCookiePath=$(COOKIE)" && echo "built binary to: $(PWD)/yesiscan"

release:
	goreleaser release --skip-validate --rm-dist
