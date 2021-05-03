package parser

import (
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/purpleidea/yesiscan/interfaces"
	"github.com/purpleidea/yesiscan/iterator"

	"github.com/purpleidea/mgmt/util/errwrap"
	"github.com/purpleidea/mgmt/util/safepath"
)

// TrivialURIParser takes input as a single string. It expects either a URL or a
// Path component as the input.
type TrivialURIParser struct {
	Debug  bool
	Logf   func(format string, v ...interface{})
	Prefix safepath.AbsDir

	Input string
}

func (obj *TrivialURIParser) String() string {
	return fmt.Sprintf("trivialuriparser(%s)", obj.Input)
}

func (obj *TrivialURIParser) Parse() ([]interfaces.Iterator, error) {
	if obj.Input == "" {
		return nil, fmt.Errorf("empty input")
	}

	iterators := []interfaces.Iterator{}

	// path component
	if strings.HasPrefix(obj.Input, "/") {
		// XXX: we could auto-detect the dir bit, and deal with rel paths too
		isDir := strings.HasSuffix(obj.Input, "/")
		info, err := os.Stat(obj.Input) // XXX: stat or Lstat?
		if err != nil {
			return nil, err
		}
		if isDir != info.IsDir() {
			return nil, fmt.Errorf("input path must end with a trailing slash if it's a dir")
		}
		path, err := safepath.ParseIntoPath(obj.Input, isDir)
		if err != nil {
			return nil, err
		}
		iterator := &iterator.Fs{
			Debug: obj.Debug,
			Logf: func(format string, v ...interface{}) {
				obj.Logf("iterator: "+format, v...)
			},
			Prefix: obj.Prefix,
			Path:   path,

			Parser: obj, // store a handle to the originator
		}
		iterators = append(iterators, iterator)
		return iterators, nil
	}

	// NOTE: it's unlikely that the url.Parse method ever errors.
	u, err := url.Parse(obj.Input)
	if err != nil {
		return nil, errwrap.Wrapf(err, "could not parse URL")
	}

	// TODO: for now, just assume it can only be a git iterator...
	iterator := &iterator.Git{
		Debug: obj.Debug,
		Logf: func(format string, v ...interface{}) {
			obj.Logf("iterator: "+format, v...)
		},
		Prefix:        obj.Prefix,
		URL:           u.String(), // TODO: pass a *net.URL instead?
		TrimGitSuffix: true,

		Parser: obj, // store a handle to the originator
	}
	iterators = append(iterators, iterator)
	return iterators, nil
}
