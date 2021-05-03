package iterator

import (
	"fmt"
	"io/fs"

	"github.com/purpleidea/yesiscan/interfaces"

	"github.com/purpleidea/mgmt/util/safepath"
)

var (
	// SkipPathExtensions is a list of file extensions to not scan. This
	// list is alphabetical and has a comment for each element.
	SkipPathExtensions = []string{
		".bmp",       // image format
		".cvsignore", // csv ignore file
		".doc",       // document format
		".eps",       // image format
		".gif",       // image format
		".gitignore", // git ignore file
		".jpeg",      // image format with weird naming
		".jpg",       // image format
		".ico",       // icon file format
		".pdf",       // document format
		".png",       // image format
		".ppt",       // presentation format (microsoft)
		".svg",       // image format
		".odp",       // presentation format (libreoffice)
		".ods",       // spreadsheet format (libreoffice)
		".odt",       // document format (libreoffice)
		".xls",       // spreadsheet format
	}

	// SkipDirPaths is a list of relative dir paths to not scan. This list
	// list is alphabetical and has a comment for each element.
	SkipDirPaths = []string{
		".git/",    // internal git folder
		".github/", // github specific stuff
		".svn/",    // internal svn folder
		//".eggs/", // python ??? directory
	}
)

// SkipPath takes an input path and file info struct, and returns whether we
// should skip over it or not. To skip it, return true and no error. To skip a
// directory, return interfaces.SkipDir as the error. Lastly, if anything goes
// wrong, you can return your own error, but minimizing this chance is ideal.
// The stuff that gets skipped in here *must* be common for all iterators, as
// this function is shared by all of them. Individual backends can have their
// own file skip detection as well. For example, one particular backend might
// not know how to scan *.go files, where as a different one might specialize in
// this. Lastly, a design decision was made to make this a "pure, stateless"
// function. In other words, the decision to skip a file or not should be based
// entirely on the input arguments, and more complicated skip functions that
// might take into account more complex logic, such as the existence of multiple
// file paths is not possible. For example, if someone were to invent a file
// called `.legalignore` that worked like `.gitignore` but told software which
// files copyrights wouldn't apply from, we'd be unable to detect those and skip
// over them with this skip function since it only has a view into individual
// files and doesn't get a stateful, full directory tree view.
func SkipPath(path safepath.Path, info fs.FileInfo) (bool, error) {

	// TODO: This could be built with a list of rules that we pass into the
	// iterator, so that it could be configurable as needed.

	if !path.IsAbs() { // the walk func gives us absolutes
		return false, fmt.Errorf("path %s was not absolute", path.String())
	}

	if info.IsDir() { // path.IsDir()
		absDir, ok := path.(safepath.AbsDir)
		if !ok { // should not happen unless bug
			return false, fmt.Errorf("expected AbsDir")
		}

		for _, dir := range SkipDirPaths {
			relDir := safepath.UnsafeParseIntoRelDir(dir)
			if absDir.HasDir(relDir) {
				return true, interfaces.SkipDir
			}
		}

		return false, nil // don't skip
	}

	absFile, ok := path.(safepath.AbsFile)
	if !ok { // should not happen unless bug
		return false, fmt.Errorf("expected AbsFile")
	}

	for _, ext := range SkipPathExtensions {
		// Make sure we have at least one char in the file name (x.foo)
		// and insensitive match on extensions like .foo that we skip.
		if absFile.HasExtInsensitive(ext) && len(ext) != len(absFile.Path()) { // case insensitive
			return true, nil
		}
	}

	return false, nil // don't skip
}
