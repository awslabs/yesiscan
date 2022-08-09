.PHONY: all build release

all: build

build:
	@go build && echo "built binary to: $(PWD)/yesiscan"

release:
	goreleaser release --skip-validate --rm-dist
