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

package iterator

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"

	"github.com/awslabs/yesiscan/interfaces"
	"github.com/awslabs/yesiscan/util/errwrap"
	"github.com/awslabs/yesiscan/util/safepath"

	//"github.com/go-git/go-git" // with go modules disabled
	// with go modules enabled (GO111MODULE=on or outside GOPATH)
	git "github.com/go-git/go-git/v5" // with go modules enabled (GO111MODULE=on or outside GOPATH)
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

const (
	// GitScheme is the standard prefix used for git repo UID's.
	GitScheme = "git://"

	// GitSchemeRaw is the standard prefix used for git repo UID's but
	// without the scheme protocol separator which is <colon-slash-slash>.
	GitSchemeRaw = "git"

	// GitProgram is the name of the git executable. It is needed until we
	// figure out how to make this pure golang.
	GitProgram = "git"
)

var (
	gitMapMutex *sync.Mutex
	gitMutexes  map[string]*sync.Mutex
	// separator is a new line character since Urls, Hash, Rev or Ref cannot
	// possibly contain new line characters and is used to make unique hashes.
	separator = "\n"
)

func init() {
	gitMapMutex = &sync.Mutex{}
	gitMutexes = make(map[string]*sync.Mutex)
}

// Git is an iterator that takes a git URL to clone and performs this download
// operation. It will eventually return an Fs iterator since there's no need for
// it to know how to walk through a filesystem tree itself. It can use a local
// cache so that future calls to the same repository won't have to waste
// bandwidth or cycles again. We don't recurse into git submodules, but rather
// the Fs iterators know how to find them and generate git iterators for them.
// This keeps things flatter and allows us to work more quickly in parallel.
//
// NOTE: I wanted to name this "giterator", but that wouldn't be consistent.
// TODO: If someone wanted to scan *every* commit, or a range of commits, rather
// than the latest HEAD, then we could have options to return multiple iterators
// to support that.
// TODO: concurrent use of this lib: https://github.com/go-git/go-git/issues/285
// NOTE: We currently keep all the repo data in one place, and have locking for
// reads and checkouts on the unique ID of the git directory.
type Git struct {
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

	// URL is the git URL of the repository that we want to clone from.
	// TODO: consider doing some clever parsing of well-known paths like
	// github-style URL's or internal company code repository URL's.
	// TODO: this could be implemented with a layered iterator that's github
	// specific, and after parsing, it returns this raw git iterator.
	URL string

	// TrimGitSuffix specifies whether we should try to trim a .git suffix
	// from any URL that we get. Usually they can be cloned both ways, but
	// modern repositories omit the need for this.
	TrimGitSuffix bool

	// Hash is the specific commit hash to use to specify what to scan. You
	// can either identify things this way, with Ref or Rev, but not more
	// than one.
	Hash string // len 40 chars

	// Ref is a specific revision to use to specify what you want to scan.
	// This can be a branch, note, remote, or tag ref. These are in the form
	// with the prefix: refs/heads/, refs/notes/, refs/remotes/ or
	// refs/tags. If you specify this, you must not specify Hash or Rev. If
	// you want the possibly ambiguous, but "easy" way of specifying
	// something, then use Rev.
	Ref string

	// Rev is the method most CLI tools use to identify a specific hash. You
	// pass in a sensible string of your choice, and git will attempt to
	// find what you mean. This isn't recommended if you want to be precise
	// because you can have weirdly named branches that can trick you.
	Rev string

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

// String returns a human-readable representation of the git repo we're looking
// at. The output of this format is not guaranteed to be constant, so don't try
// to parse it.
func (obj *Git) String() string {
	x := "HEAD"
	if obj.Rev != "" {
		x = fmt.Sprintf("rev(%s)", obj.Rev)
	}
	if obj.Ref != "" {
		x = fmt.Sprintf("ref(%s)", obj.Ref)
	}
	if obj.Hash != "" {
		x = fmt.Sprintf("hash(%s)", obj.Hash)
	}
	return fmt.Sprintf("git: %s @ %s", obj.URL, x)
}

// Validate runs some checks to ensure this iterator was built correctly.
func (obj *Git) Validate() error {
	if obj.Logf == nil {
		return fmt.Errorf("the Logf function must be specified")
	}
	if err := obj.Prefix.Validate(); err != nil {
		return err
	}

	if obj.URL == "" {
		return fmt.Errorf("must specify a URL")
	}

	if strings.Contains(obj.URL, separator) {
		return fmt.Errorf("provided URL is invalid")
	}

	if _, err := url.Parse(obj.URL); err != nil {
		return err // not that url.Parse ever really errors :/
	}

	if obj.Hash != "" {
		if err := obj.validateHash(); err != nil {
			return err
		}
	}
	// TODO: can we validate ref somehow?

	if obj.Rev != "" {
		if err := obj.validateRef(); err != nil {
			return err
		}
	}
	if obj.Rev != "" {
		if err := obj.validateRev(); err != nil {
			return err
		}
	}

	// At most one must be true.
	a := obj.Hash != ""
	b := obj.Ref != ""
	c := obj.Rev != ""
	if (a || b || c) && !xor(a, b, c) {
		return fmt.Errorf("you may specify Hash, Ref or Rev, but not more than one")
	}

	return nil
}

// validateRef validates the Ref specifically.
func (obj *Git) validateRef() error {
	if obj.Ref == "" {
		return fmt.Errorf("empty ref")
	}
	if strings.Contains(obj.Ref, separator) {
		return fmt.Errorf("provided ref is invalid")
	}
	if obj.Ref == plumbing.HEAD.String() {
		return nil
	}
	main := plumbing.NewBranchReferenceName("main") // not upstream
	if obj.Ref == plumbing.Master.String() || obj.Ref == main.String() {
		return nil
	}

	name := plumbing.ReferenceName(obj.Ref)
	if name.IsBranch() || name.IsNote() || name.IsRemote() || name.IsTag() {
		return nil
	}

	return fmt.Errorf("unknown ref: %s", obj.Ref)
}

// validateRev validates the Rev specifically.
func (obj *Git) validateRev() error {
	if obj.Rev == "" {
		return fmt.Errorf("empty rev")
	}
	if strings.Contains(obj.Rev, separator) {
		return fmt.Errorf("provided rev is invalid")
	}

	return nil
}

// validateHash validates the Hash specifically.
func (obj *Git) validateHash() error {
	if obj.Hash == "" {
		return fmt.Errorf("empty hash")
	}
	if strings.Contains(obj.Hash, separator) {
		return fmt.Errorf("provided hash is invalid")
	}
	if !plumbing.IsHash(obj.Hash) { // IsHash checks if len is 40
		return fmt.Errorf("provided hash is invalid")
	}

	return nil
}

// GetParser returns a handle to the parent parser that built this iterator if
// there is one.
func (obj *Git) GetParser() interfaces.Parser { return obj.Parser }

// GetIterator returns a handle to the parent iterator that built this iterator
// if there is one.
func (obj *Git) GetIterator() interfaces.Iterator { return obj.Iterator }

// Recurse runs a simple iterator that is responsible for cloning a git
// repository into a local filesystem path. If this happens successfully, it
// will return a new FsIterator that is initialized to this root path.
func (obj *Git) Recurse(ctx context.Context, scan interfaces.ScanFunc) ([]interfaces.Iterator, error) {
	relDir := safepath.UnsafeParseIntoRelDir("git/")
	prefix := safepath.JoinToAbsDir(obj.Prefix, relDir)
	if err := os.MkdirAll(prefix.Path(), interfaces.Umask); err != nil {
		return nil, err
	}

	// make a unique ID for the directory
	// XXX: we can consider different algorithms or methods here later...
	// uniqueString is used to make a unique hash to store repositories with a
	// different hash or ref or rev separately.
	uniqueString := obj.URL + separator + obj.Hash + separator + obj.Ref + separator + obj.Rev
	sum := sha256.Sum256([]byte(uniqueString))
	hashRelDir, err := safepath.ParseIntoRelDir(fmt.Sprintf("%x", sum))
	if err != nil {
		return nil, err
	}
	repoAbsDir := safepath.JoinToAbsDir(prefix, hashRelDir)

	gitMapMutex.Lock()
	mu, exists := gitMutexes[obj.URL]
	if !exists {
		mu = &sync.Mutex{}
		gitMutexes[obj.URL] = mu
	}
	gitMapMutex.Unlock()

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

	obj.Logf("cloning %s into %s", obj.String(), repoAbsDir)

	directory := repoAbsDir.Path()
	isBare := false
	repository, err := git.PlainCloneContext(ctx, directory, isBare, &git.CloneOptions{
		URL: obj.URL,
		// Don't recurse, we do it manually with the FsIterator, as this
		// way we'll get all the repositories cloned next to each other,
		// instead of in a big recursive filesystem tree.
		RecurseSubmodules: git.NoRecurseSubmodules,
		//Auth transport.AuthMethod
		//Progress: os.Stdout,

	})
	if err == git.ErrRepositoryAlreadyExists {
		obj.Logf("repo %s already exists", obj.String())
		repository, err = git.PlainOpenWithOptions(directory, &git.PlainOpenOptions{})
		if err != nil {
			obj.unlock()
			return nil, errwrap.Wrapf(err, "error opening repository")
		}

	} else if err != nil {
		obj.unlock()
		return nil, errwrap.Wrapf(err, "error cloning repository %s", obj.String())
	}

	var hash plumbing.Hash

	if obj.Hash != "" {
		hash = plumbing.NewHash(obj.Hash)
	}

	if obj.Ref != "" {
		// TODO: update with https://github.com/go-git/go-git/issues/289
		var err error
		ref := plumbing.ReferenceName(obj.Ref)
		if hash, err = getCommitFromRef(repository, ref); err != nil {
			obj.unlock()
			return nil, err
		}
	}

	if obj.Rev != "" {
		// This API returns a *Hash. Everything else uses a Hash as-is.
		pHash, err := repository.ResolveRevision(plumbing.Revision(obj.Rev))
		if err != nil {
			obj.unlock()
			return nil, err
		}
		hash = *pHash
	}

	// XXX: We're currently commenting this out and using a `git cli` exec
	// because it's unclear how to do this properly with this library. When
	// we figure it out, we should get rid of the below exec code!
	//if hash.IsZero() { // desired state not specified, use HEAD branch...
	//	// XXX: I'm not sure this is the correct way to find the HEAD.
	//	// XXX: eg: https://github.com/ansible/ansible uses `devel`.
	//	// XXX: running: `git remote show origin`, shows: `HEAD branch: <branch>`
	//	// XXX: how do I extract that without looking in: ~/.git/refs/remotes/origin/*
	//	names := []string{"HEAD", "master", "main"}
	//	var err error
	//	var name string
	//	for _, name = range names {
	//		ref := plumbing.NewRemoteReferenceName("origin", name)
	//		// git symbolic-ref refs/remotes/origin/HEAD ?
	//		hash, err = getCommitFromRef(repository, ref)
	//		if err == plumbing.ErrReferenceNotFound {
	//			continue // this error means we continue
	//		}
	//		break // if a different error or a nil, we are done!
	//	}
	//	if err != nil { // check here if we found something!
	//		obj.unlock()
	//		return nil, errwrap.Wrapf(err, "could not find default HEAD in origin")
	//	}
	//	obj.Logf("default HEAD is at: %s", name)
	//}

	if hash.IsZero() {
		// git symbolic-ref refs/remotes/origin/HEAD			# doesn't work in this clone!
		// git remote show origin | grep 'HEAD branch' | cut -d' ' -f5	# does work

		args := []string{"remote", "show", "origin"}

		prog := fmt.Sprintf("%s %s", GitProgram, strings.Join(args, " "))

		// TODO: add a progress bar of some sort somewhere
		if obj.Debug {
			obj.Logf("running: %s", prog)
		}

		// TODO: do we need to do the ^C handling?
		// XXX: is the ^C context cancellation propagating into this correctly?
		cmd := exec.CommandContext(ctx, GitProgram, args...)

		cmd.Dir = directory
		cmd.Env = []string{}

		// ignore signals sent to parent process (we're in our own group)
		cmd.SysProcAttr = &syscall.SysProcAttr{
			Setpgid: true,
			Pgid:    0,
		}

		out, reterr := cmd.Output()
		if reterr != nil {
			if obj.Debug {
				obj.Logf("error running: %s", prog)
			}
			return nil, errwrap.Wrapf(reterr, "error running: %s", prog)
		}

		buffer := bytes.NewBuffer(out)

		// NOTE: we don't need to worry about `token too long` errors
		// since we expect only a small amount of command output here
		scanner := bufio.NewScanner(buffer)
		prefix := "HEAD branch: "
		name := ""
		for scanner.Scan() {
			s := strings.TrimSpace(scanner.Text())
			if !strings.HasPrefix(s, prefix) {
				continue
			}
			name = s[len(prefix):]
			break
		}
		if err := scanner.Err(); err != nil {
			obj.unlock()
			return nil, errwrap.Wrapf(err, "could not read git command output")
		}
		if name == "" {
			obj.unlock()
			return nil, fmt.Errorf("could not find default HEAD in remote origin list")
		}

		ref := plumbing.NewRemoteReferenceName("origin", name)
		// git symbolic-ref refs/remotes/origin/HEAD ?
		hash, err = getCommitFromRef(repository, ref)
		//if err == plumbing.ErrReferenceNotFound
		// just deal with any error...
		if err != nil { // check here if we found something!
			obj.unlock()
			return nil, errwrap.Wrapf(err, "could not find default HEAD in origin")
		}
		obj.Logf("default HEAD is at: %s", name)
	}

	head, err := repository.Head()
	if err != nil {
		obj.unlock()
		return nil, err
	}
	if obj.Debug {
		obj.Logf("HEAD is at: %s", head.Hash())
	}

	// If the desired state, is not equal to the actual state, set it!
	if hash.String() != head.Hash().String() {
		worktree, err := repository.Worktree()
		if err != nil {
			obj.unlock()
			return nil, err
		}

		checkoutOptions := &git.CheckoutOptions{}
		if obj.Hash != "" {
			checkoutOptions.Hash = hash
		}
		// We use the consistent hash approach to identify the repo so
		// that we have a unique identifier to use everywhere...
		//if obj.Ref != "" {
		//	checkoutOptions.Branch = plumbing.ReferenceName(obj.Ref)
		//}

		if err := worktree.Checkout(checkoutOptions); err != nil {
			obj.unlock()
			return nil, err
		}
	}

	obj.iterators = []interfaces.Iterator{}

	u, err := url.Parse(obj.URL) // build a url to modify
	if err != nil {
		obj.unlock()
		return nil, err
	}
	u.Scheme = GitSchemeRaw
	u.Opaque = ""                         // encoded opaque data
	if _, has := u.User.Password(); has { // redact password
		u.User = url.UserPassword(u.User.Username(), "")
	}
	//u.Host = ? // host or host:port

	// remove the .git suffix of old-style repos
	if obj.TrimGitSuffix && strings.HasSuffix(u.Path, ".git") {
		u.Path = strings.TrimSuffix(u.Path, ".git")
	}

	u.RawPath = ""       // encoded path hint (see EscapedPath method)
	u.ForceQuery = false // append a query ('?') even if RawQuery is empty
	v := url.Values{}
	v.Set("sha1", hash.String())
	u.RawQuery = v.Encode() // encoded query values, without '?'
	u.Fragment = ""         // fragment for references, without '#'
	u.RawFragment = ""      // encoded fragment hint (see EscapedFragment method)

	iterator := &Fs{
		Debug: obj.Debug,
		Logf: func(format string, v ...interface{}) {
			obj.Logf(format, v...) // TODO: add a prefix?
		},
		Prefix: obj.Prefix,

		Iterator: obj,

		Path: repoAbsDir,

		GenUID: func(safePath safepath.Path) (string, error) {
			if !safepath.HasPrefix(safePath, repoAbsDir) {
				// programming error
				return "", fmt.Errorf("path doesn't have prefix")
			}

			p := ""
			// remove repoAbsDir prefix from safePath to get a relPath
			relPath, err := safepath.StripPrefix(safePath, repoAbsDir)
			if err == nil {
				p = relPath.String()
			} else if err != nil && safePath.String() != repoAbsDir.String() {
				// programming error
				return "", errwrap.Wrapf(err, "problem stripping prefix")
			}

			x := *u // copy
			x.Path += "/" + p

			return x.String(), nil
		},

		//Unlock: unlock,
	}
	obj.iterators = append(obj.iterators, iterator)

	return obj.iterators, nil
}

// Close shuts down the iterator and/or performs clean up after the Recurse
// method has run. This must be called if you run Recurse.
func (obj *Git) Close() error {
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

// modified from: https://github.com/go-git/go-git/blob/2f7c4ae04d62705c98db0cf900410b5e6f6d5021/worktree.go#L211
// formerly: func (w *Worktree) getCommitFromCheckoutOptions(opts *CheckoutOptions) (plumbing.Hash, error)
func getCommitFromRef(repository *git.Repository, ref plumbing.ReferenceName) (plumbing.Hash, error) {
	resolved := true
	b, err := repository.Reference(ref, resolved)
	if err != nil {
		return plumbing.ZeroHash, err
	}

	if !b.Name().IsTag() {
		return b.Hash(), nil
	}

	o, err := repository.Object(plumbing.AnyObject, b.Hash())
	if err != nil {
		return plumbing.ZeroHash, err
	}

	switch o := o.(type) {
	case *object.Tag:
		if o.TargetType != plumbing.CommitObject {
			return plumbing.ZeroHash, fmt.Errorf("unsupported tag object target %q", o.TargetType)
		}

		return o.Target, nil
	case *object.Commit:
		return o.Hash, nil
	}

	return plumbing.ZeroHash, fmt.Errorf("unsupported tag target %q", o.Type())
}

// xor is a logical bool.
func xor(bools ...bool) bool {
	found := false
	for _, b := range bools {
		if !b {
			continue
		}
		// b is true!
		if found {
			// already found
			return false
		}
		found = true
	}

	return found
}
