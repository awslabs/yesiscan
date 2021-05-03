gofmt:
	# TODO: remove gofmt once goimports has a -s option
	find . -maxdepth 9 -type f -name '*.go' -not -path './old/*' -not -path './tmp/*' -not -path './vendor/*' -exec gofmt -s -w {} \;
	find . -maxdepth 9 -type f -name '*.go' -not -path './old/*' -not -path './tmp/*' -not -path './vendor/*' -exec goimports -w {} \;
