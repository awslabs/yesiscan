// Copyright Amazon.com Inc or its affiliates and the project contributors
// Written by James Shubin <purple@amazon.com> and the project contributors
//
// Licensed under the Apache License, Version 2.0 (the "License"); you may not
// use this file except in compliance with the License. You may obtain a copy of
// the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS, WITHOUT
// WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the
// License for the specific language governing permissions and limitations under
// the License.
//
// We will never require a CLA to submit a patch. All contributions follow the
// `inbound == outbound` rule.
//
// This is not an official Amazon product. Amazon does not offer support for
// this project.
//
// SPDX-License-Identifier: Apache-2.0

package iterator

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/awslabs/yesiscan/interfaces"
	"github.com/awslabs/yesiscan/util/errwrap"
	"github.com/awslabs/yesiscan/util/safepath"
)

const (
	// TarExtension is the standard extension used for tar URI's.
	TarExtension = ".tar"
)

var (
	tarMapMutex *sync.Mutex
	tarMutexes  map[string]*sync.Mutex
)

func init() {
	tarMapMutex = &sync.Mutex{}
	tarMutexes = make(map[string]*sync.Mutex)
}

// Tar is an iterator that takes a .tar URI to open and performs the un-tar
// operation. It will eventually return an Fs iterator since there's no need for
// it to know how to walk through a filesystem tree itself. It can use a local
// cache so that future calls to the same URI won't have to waste cycles, but
// only in cases when we can determine it will be the same file. This currently
// only unpacks files and directories. Any other file type (like symlinks) will
// be ignored.
type Tar struct {
	Debug  bool
	Logf   func(format string, v ...interface{})
	Prefix safepath.AbsDir

	// Parser is a pointer to the parser that returned this. If it wasn't
	// returned by a parser, leave this nil. If this iterator came from an
	// iterator, then the Iterator handle should be filled instead.
	Parser interfaces.Parser

	// Iterator is a pointer to the iterator that returned this. If it
	// wasn't returned by an iterator, leave this nil. If this iterator came
	// from a parser, then the Parser handle should be filled instead.
	Iterator interfaces.Iterator

	// Path is the location of the file to untar.
	Path safepath.AbsFile

	// FIXME: add tar max file limit field to prevent tar bombs

	// AllowAnyExtension specifies whether we will attempt to run if the
	// Path does not end with the correct tar extension.
	AllowAnyExtension bool

	// AllowedExtensions specifies a list of extensions that we are allowed
	// to try to decode from. If this is empty, then we allow only the
	// default of tar because allowing no extensions at all would make no
	// sense. If AllowAnyExtension is set, then this has no effect. All the
	// matches are case insensitive.
	AllowedExtensions []string

	// iterators store the list of which iterators we created, so we know
	// which ones we have to close!
	iterators []interfaces.Iterator

	// unlock is a function that should be called as part of the Close
	// method once this resource is finished. It can be defined when
	// building this iterator in case we want a mechanism for the caller of
	// this iterator to tell the child when to unlock any in-use resources.
	// It must be safe to call this function more than once if necessary.
	// This is currently used privately.
	unlock func()
}

// String returns a human-readable representation of the tar path we're looking
// at. The output of this format is not guaranteed to be constant, so don't try
// to parse it.
func (obj *Tar) String() string {
	return fmt.Sprintf("tar: %s", obj.Path)
}

// Validate runs some checks to ensure this iterator was built correctly.
func (obj *Tar) Validate() error {
	if obj.Logf == nil {
		return fmt.Errorf("the Logf function must be specified")
	}
	if err := obj.Prefix.Validate(); err != nil {
		return err
	}

	if obj.Path.Path() == "" {
		return fmt.Errorf("must specify a Path")
	}

	return obj.validateExtension()
}

// validateExtension is a helper function to process our extension validation.
func (obj *Tar) validateExtension() error {
	if obj.AllowAnyExtension {
		return nil
	}
	if obj.Path.HasExtInsensitive(TarExtension) && len(obj.AllowedExtensions) == 0 {
		return nil
	}

	for _, x := range obj.AllowedExtensions {
		if obj.Path.HasExtInsensitive(x) {
			return nil
		}
	}

	if len(obj.AllowedExtensions) == 0 {
		return fmt.Errorf("the tar extension is required without the allow any extension option")
	}

	return fmt.Errorf("an allowed extension is required to run this iterator")
}

// GetParser returns a handle to the parent parser that built this iterator if
// there is one.
func (obj *Tar) GetParser() interfaces.Parser { return obj.Parser }

// GetIterator returns a handle to the parent iterator that built this iterator
// if there is one.
func (obj *Tar) GetIterator() interfaces.Iterator { return obj.Iterator }

// Recurse runs a simple iterator that is responsible for untar-ing a tar URI
// into a local filesystem path. If this happens successfully, it will return a
// new FsIterator that is initialized to this root path.
func (obj *Tar) Recurse(ctx context.Context, scan interfaces.ScanFunc) ([]interfaces.Iterator, error) {
	relDir := safepath.UnsafeParseIntoRelDir("tar/")
	prefix := safepath.JoinToAbsDir(obj.Prefix, relDir)
	if err := os.MkdirAll(prefix.Path(), interfaces.Umask); err != nil {
		return nil, err
	}

	// make a unique ID for the directory
	// XXX: we can consider different algorithms or methods here later...
	now := strconv.FormatInt(time.Now().UnixMilli(), 10) // itoa but int64
	sum := sha256.Sum256([]byte(obj.Path.Path() + now))
	hashRelDir, err := safepath.ParseIntoRelDir(fmt.Sprintf("%x", sum))
	if err != nil {
		return nil, err
	}
	// ensure it gets put into a folder so it doesn't explode current dir
	tarAbsDir := safepath.JoinToAbsDir(prefix, hashRelDir)

	tarMapMutex.Lock()
	mu, exists := tarMutexes[obj.Path.Path()]
	if !exists {
		mu = &sync.Mutex{}
		tarMutexes[obj.Path.Path()] = mu
	}
	tarMapMutex.Unlock()

	if obj.Debug {
		obj.Logf("locking: %s", obj.String())
	}
	mu.Lock() // locking happens here (unlock on all errors/returns!)
	once := &sync.Once{}
	obj.unlock = func() {
		fn := func() {
			if obj.Debug {
				obj.Logf("unlocking: %s", obj.String())
			}
			mu.Unlock()
		}
		once.Do(fn)
	}

	// XXX: unlock when context closes?

	// XXX: If the destination dir has contents, consider removing them
	// first. This is one reason why we have a mutex.

	f, err := os.Open(obj.Path.Path())
	if err != nil {
		obj.unlock()
		return nil, errwrap.Wrapf(err, "error opening path %s", obj.Path)
	}
	defer f.Close()

	// Open the tar archive for reading.
	// FIXME: use a variant that can take a context
	z := tar.NewReader(f)
	//defer z.Close() // doesn't exist, magic happens in Next()!

	filesTotal := 0
	bytesTotal := int64(0)
	emptyTotal := 0
	// Iterate through the files in the archive.
	// XXX: can a child directory appear before a parent?
	// TODO: add a recurring progress logf if it takes longer than 30 sec
	for {
		// In an effort to short-circuit things if needed, we run a
		// check ourselves and break out early if we see that we have
		// cancelled early.
		select {
		case <-ctx.Done():
			obj.unlock()
			return nil, errwrap.Wrapf(ctx.Err(), "ended untar-ing early")
		default:
		}

		// From the docs on Next():
		//
		// Next advances to the next entry in the tar archive. The
		// Header.Size determines how many bytes can be read for the
		// next file. Any remaining data in the current file is
		// automatically discarded.
		// io.EOF is returned at the end of the input.
		//
		// I would expect to call Next _after_ first reading a file, but
		// the tests show otherwise and you wouldn't have the Header at
		// the start before you did the read, so I guess this is a docs
		// bug.
		header, err := z.Next()
		if err == io.EOF {
			break // End of archive
		}
		if err != nil {
			obj.unlock()
			return nil, errwrap.Wrapf(err, "unknown tar error on Next")
		}
		if header == nil {
			// FIXME: can this happen if at all?
			obj.Logf("tar header at index %d is empty", emptyTotal+filesTotal)
			emptyTotal++
			continue
		}

		if obj.Debug {
			obj.Logf("tar has format: %s", header.Format.String())
		}

		// TODO: can header.Name be empty?
		name := header.Name
		newName := name
		if name != "" {
			obj.Logf("tar: %s", name)
		} else {
			// TODO: is this even possible for tar files?
			// a .tar might have no name string for example
			obj.Logf("tar name is empty")
			newName = fmt.Sprintf("unknown-%d", filesTotal)
			p := obj.Path.Path()
			suffix := WhichSuffixInsensitive(p, []string{TarExtension})
			p = strings.TrimSuffix(p, suffix)
			ix := strings.LastIndex(p, "/")
			if ix != -1 {
				p = p[ix+1:]
				if len(p) > 0 {
					newName = p
				}
				obj.Logf("tar basename: %s", newName)
			}
		}

		fileInfo := header.FileInfo()
		// TODO: obj.Debug ?

		// do we ever disagree on what a dir is?
		if header.Typeflag == tar.TypeDir != fileInfo.IsDir() {
			obj.Logf("tar: please report this unusual tar file to the author")
			obj.Logf("tar: filename: %s", obj.Path.Path())
		}

		//if fileInfo.IsDir() {
		if header.Typeflag == tar.TypeDir {
			relDir, err := safepath.ParseIntoRelDir(newName)
			if err != nil {
				// programming error
				obj.unlock()
				return nil, err
			}

			// this is where the new dir will be created
			absDir := safepath.JoinToAbsDir(tarAbsDir, relDir)

			// XXX: sanity check (is output in the dir?)
			// TODO: we could add this, but safepath automatically does this
			// if absDir is not inside of tarAbsDir then error

			// XXX: which mode method?
			//if err := os.MkdirAll(absDir.Path(), fileInfo.Mode()); err != nil {
			if err := os.MkdirAll(absDir.Path(), os.ModePerm); err != nil {
				// programming error
				obj.unlock()
				return nil, err
			}

			continue
		} else if header.Typeflag != tar.TypeReg {
			// Type '0' indicates a regular file.
			// TypeReg = '0'
			// Type '1' to '6' are header-only flags and may not
			// have a data body.
			// TypeLink    = '1' // Hard link
			// TypeSymlink = '2' // Symbolic link
			// TypeChar    = '3' // Character device node
			// TypeBlock   = '4' // Block device node
			// TypeDir     = '5' // Directory
			// TypeFifo    = '6' // FIFO node
			// Type '7' is reserved.
			// TypeCont = '7'
			obj.Logf("tar: skipping file of type: %v", header.Typeflag)
			continue
		}

		relFile, err := safepath.ParseIntoRelFile(newName)
		if err != nil {
			// programming error
			obj.unlock()
			return nil, err
		}

		// this is where the output file will be stored
		absFile := safepath.JoinToAbsFile(tarAbsDir, relFile)

		// XXX: sanity check (is output in the dir?)
		// TODO: we could add this, but safepath automatically does this
		// if absFile is not inside of tarAbsDir then error

		absDir := absFile.Dir() // get the absDir that absFile is in

		// XXX: which mode to use? Maybe we are assuming a mode here
		// because we haven't seen that dir yet! Maybe if we pre-sort
		// all of the tar file entries first...
		//if err := os.MkdirAll(absDir.Path(), x.Mode()); err != nil {
		if err := os.MkdirAll(absDir.Path(), os.ModePerm); err != nil {
			// programming error
			obj.unlock()
			return nil, err
		}

		// write to this location
		dest, err := os.OpenFile(absFile.Path(), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, os.ModePerm)
		if err != nil {
			obj.unlock()
			return nil, errwrap.Wrapf(err, "error writing our file to disk at %s", absFile)
		}
		// don't `defer` close here because we want to free in the loop

		// FIXME: use a variant that can take a context
		size, err := io.Copy(dest, z)
		if err != nil {
			dest.Close() // close dest file on error!
			obj.unlock()
			return nil, errwrap.Wrapf(err, "error writing our file to disk at %s", absFile)
		}
		obj.Logf("untar-ed: %d bytes to disk at %s", size, absFile)

		dest.Close() // close dest file on error!

		filesTotal++
		bytesTotal += int64(size)
	}

	// TODO: change to human readable bytes
	obj.Logf("untar-ed: %d files from %s into %s (%d bytes)", filesTotal, obj.String(), tarAbsDir, bytesTotal)

	obj.iterators = []interfaces.Iterator{}

	// if it's a single tar file we return an fs iterator and let the fs
	// iterator sort that out...
	iterator := &Fs{
		Debug: obj.Debug,
		Logf: func(format string, v ...interface{}) {
			obj.Logf(format, v...) // TODO: add a prefix?
		},
		Prefix: obj.Prefix,

		Iterator: obj,

		Path: tarAbsDir,

		//Unlock: unlock,
	}
	obj.iterators = append(obj.iterators, iterator)

	return obj.iterators, nil
}

// Close shuts down the iterator and/or performs clean up after the Recurse
// method has run. This must be called if you run Recurse.
func (obj *Tar) Close() error {
	if obj.unlock != nil {
		obj.unlock()
	}
	var errs error
	for i := len(obj.iterators) - 1; i >= 0; i-- { // reverse order (stacks!)
		if err := obj.iterators[i].Close(); err != nil {
			errs = errwrap.Append(errs, err)
		}
	}
	return errs
}
