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

package lib

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/awslabs/yesiscan/interfaces"
	"github.com/awslabs/yesiscan/util/errwrap"
	"github.com/awslabs/yesiscan/util/safepath"
)

// Core is the core runner logic that is used in Main to achieve the desired
// result that you want. It is implemented this way so that it can be reused
// from multiple different frontends including a CLI, LIB, API, WEBUI and BOTUI.
type Core struct {
	Debug bool
	Logf  func(format string, v ...interface{})

	// Backends represents the list of backends to run for this execution.
	// In particular, there's nothing stopping you from initializing the
	// same backend multiple times with different input parameters, as long
	// as it was designed to be thread-safe.
	Backends        []interfaces.Backend
	Iterators       []interfaces.Iterator // TODO: should this be passed into Run instead?
	ShutdownOnError bool
}

// Init initializes and validates the core struct before use.
func (obj *Core) Init(ctx context.Context) error {
	i := 0 // count first so we get a more accurate validation message
	for _, backend := range obj.Backends {
		_, ok := backend.(interfaces.ValidateBackend)
		if !ok {
			continue
		}
		i++
	}
	obj.Logf("validating %d backends...", i)
	for _, backend := range obj.Backends {
		vb, ok := backend.(interfaces.ValidateBackend)
		if !ok {
			continue
		}
		if err := vb.Validate(ctx); err != nil {
			return errwrap.Wrapf(err, "backend %s validate failed", vb.String())
		}
	}

	return nil
}

// Run launches each iterator and passes in a scan function that runs over
// (loops over) each backend.
//
// To run this in parallel, simplistically, it would mean that each backend
// would effectively perform its own iteration, and that each file will probably
// get loaded separately into memory from disk, which is inefficient. The
// advantage is that it's architecturally easier, and it allows us to easily
// drop-in any backend. If a backend can be made to supports more precise
// interfaces, then we can iterate over the entire filesystem once, and share
// that iteration with more than one backend. Ideally, we would initialize each
// backend and keep it running as a function that accepts a stream of work to
// process... There's also no reason that we can't even add the same backend in
// twice with different params passed to it, as long as each is thread-safe and
// doesn't incorrectly misuse global state.
func (obj *Core) Run(ctx context.Context) (interfaces.ResultSet, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel() // can be safely called more than once

	iterators := []interfaces.Iterator{} // list of all iterators
	iterators = append(iterators, obj.Iterators...)

	scanners := make(chan *Scanner) // list of all scanners (one for each iterator)

	allResultSets := make(map[string]map[interfaces.Backend]*interfaces.Result)
	resultErrors := []error{}

	wg := &sync.WaitGroup{}
	defer wg.Wait()
	wg.Add(1)
	go func() { // collect results in parallel so we don't block an iterator
		defer wg.Done()
		i := -1
		for scanner := range scanners { // receive
			i++ // counter
			if obj.Debug {
				obj.Logf("result(%d) wait", i)
			}
			results, err := scanner.Result() // these contain a wg
			if obj.Debug {
				obj.Logf("result(%d) done", i)
			}
			if err != nil {
				resultErrors = append(resultErrors, err)
			}
			// done scanning, so unlock this!
			if err := iterators[i].Close(); err != nil {
				resultErrors = append(resultErrors, err)
			}

			for _, m := range results {
				for _, result := range m {
					// tag (annotate) the result
					tagResultIterator(result, iterators[i])
				}
			}

			// inefficient, but fine for now
			allResultSets, err = interfaces.MergeResultSets(allResultSets, results)
			if err != nil {
				resultErrors = append(resultErrors, err)
			}
		}
	}()

	obj.Logf("starting with %d iterators...", len(iterators))
	obj.Logf("running over %d backends...", len(obj.Backends))
	errors := []error{}
	once := &sync.Once{}
	closeFnDo := func() { close(scanners) }
	closeFn := func() { once.Do(closeFnDo) }
	defer closeFn()
	for i := 0; len(iterators) > i; i++ { // while
		x := iterators[i]
		defer func() {
			// TODO: capture err and return it.
			x.Close()
		}()

		// helper function builder/wrapper to run backend Scan* functions
		scanner := &Scanner{
			Debug: obj.Debug,
			Logf: func(format string, v ...interface{}) {
				obj.Logf("scanner: "+format, v...)
			},

			Backends: obj.Backends,
		}
		if err := scanner.Init(); err != nil {
			return nil, errwrap.Wrapf(err, "scanner init failed")
		}
		defer scanner.Result() // Wait()
		//scanners = append(scanners, scanner)

		if obj.Debug {
			obj.Logf("running iterator(%d): %s", i, x)
		}
		if err := x.Validate(); err != nil {
			return nil, errwrap.Wrapf(err, "iterator validate failed")
		}

		// Mechanism to end this long iterator loop early if needed...
		// In an effort to short-circuit things if needed, we run a
		// check ourselves and break out early if we see that we have
		// cancelled early.
		select {
		case <-ctx.Done():
			errors = append(errors, ctx.Err())
			break
		default:
		}

		if obj.Debug {
			obj.Logf("recurse(%d) start", i)
		}
		it, err := x.Recurse(ctx, scanner.Scan)
		if obj.Debug {
			obj.Logf("recurse(%d) done", i)
		}
		if err != nil {
			if obj.ShutdownOnError {
				// this will trigger the ctx cancel() in defer
				return nil, errwrap.Wrapf(err, "recurse error with: %s", x)
			}
			errors = append(errors, err)
			continue
		}
		// don't unlock here in case something is running in parallel...

		// We wait until *after* recurse has finished running before we
		// send the signal on the channel, because once we do, the
		// results method of the scanner will be run, which we should
		// only do *after* the results are ready.
		select {
		case scanners <- scanner: // send
		case <-ctx.Done():
			errors = append(errors, ctx.Err())
			break
		}

		iterators = append(iterators, it...)
	}
	closeFn() // done sending on channel
	//obj.wg.Wait() // wait for everything to finish

	wg.Wait()                                // wait for goroutine to exit
	errors = append(errors, resultErrors...) // from the goroutine

	if len(errors) > 0 {
		var ea error
		for _, e := range errors {
			ea = errwrap.Append(ea, e)
		}
		return nil, errwrap.Wrapf(ea, "core run errored")
	}

	return allResultSets, nil
}

// Scanner is functionality that encapsulates the running of each backend. It
// builds and provides a generic scan mechanism that can be easily passed to the
// core logic for reuse. Concurrent running of each backend happens in here, and
// a caching layer for results is also available within.
type Scanner struct {
	Debug bool
	Logf  func(format string, v ...interface{})

	Backends []interfaces.Backend

	wg *sync.WaitGroup
	mu *sync.Mutex

	// results could have instead been represented with the path second, as:
	//	results map[interfaces.Backend]map[string]*interfaces.Result
	// but I instead decided that it would be more efficient this way since
	// we will want to do all the fancy grouping along path rules and not
	// backends. Once we have a set of paths, we then look at the backends.
	results interfaces.ResultSet // guarded by the mutex

	// skipdirs represents a list of dir paths that backends have told us to
	// skip over. We cache these to avoid unnecessarily asking the backends.
	skipdirs map[interfaces.Backend]map[string]struct{}
}

// Init initializes the scanner struct before use.
func (obj *Scanner) Init() error {
	obj.wg = &sync.WaitGroup{}
	obj.mu = &sync.Mutex{}

	obj.results = make(interfaces.ResultSet)

	obj.skipdirs = make(map[interfaces.Backend]map[string]struct{})
	for _, backend := range obj.Backends {
		_, ok1 := backend.(interfaces.DataBackend)
		_, ok2 := backend.(interfaces.PathBackend)
		_, ok3 := backend.(interfaces.RootBackend)
		_, ok4 := backend.(interfaces.SeekBackend)
		if !ok1 && !ok2 && !ok3 && !ok4 {
			return fmt.Errorf("invalid backend: %s", backend.String())
		}
		if !ok1 && !ok2 { // TODO: remove this when we implement them!
			return fmt.Errorf("the RootBackend and SeekBackend is not yet supported")
		}

		obj.skipdirs[backend] = make(map[string]struct{})
	}

	return nil
}

// Scan runs the correct scanning function of each backend. This function will
// get called in parallel, by multiple different iterators. As a result, it must
// be thread-safe. This function is passed in to the iterators by Core.
func (obj *Scanner) Scan(ctx context.Context, path safepath.Path, info *interfaces.Info) error {
	errors := []error{}
	mu := &sync.Mutex{} // guards list of errors
	wg := &sync.WaitGroup{}

	// TODO: we could switch and avoid doing this if we knew that
	// zero backends were going to need it, but we know most will,
	// so avoid optimizing early, and skip pre-checking for this.
	var data []byte
	var err error
	if !info.FileInfo.IsDir() {
		data, err = os.ReadFile(path.Path())
		if err != nil {
			return err // TODO: errwrap?
		}
	}

Loop:
	for _, backend := range obj.Backends {
		// Some backends aren't particularly well-behaved with
		// regards to obeying the context cancellation signal.
		// In an effort to short-circuit things if needed, we
		// run a check ourselves and break out early if we see
		// that we have cancelled early. This change improves
		// cancellation latency significantly.
		select {
		case <-ctx.Done():
			errors = append(errors, ctx.Err())
			break Loop
		default:
		}

		// TODO: add a counting semaphore if it's desired
		wg.Add(1)
		obj.wg.Add(1)
		go func(backend interfaces.Backend) {
			defer wg.Done()
			defer obj.wg.Done()

			if obj.Debug {
				obj.Logf("scanning: %s", path)
			}

			var result *interfaces.Result
			var err error

			// XXX: cache lookups here?
			//	if x, ok := backend.(interfaces.CachedDataBackend); ok {
			//		result, err = x.LookupData(ctx, data, info)
			//	} else if x, ok := backend.(interfaces.CachedPathBackend); ok {
			//		result, err = x.LookupPath(ctx, path, info)
			//	}
			// XXX: if err, look at cache policy, otherwise continue or err

			if _, exists := obj.skipdirs[backend][info.UID]; info.FileInfo.IsDir() && exists {
				if obj.Debug {
					obj.Logf("skip dir: %s", path)
				}
				return
			}

			// XXX: wrap these in a helper function
			if x, ok := backend.(interfaces.DataBackend); ok {
				//if len(data) == 0 { // possible directory
				//	return // skip directories!
				//}
				result, err = x.ScanData(ctx, data, info)
			} else if x, ok := backend.(interfaces.PathBackend); ok {
				result, err = x.ScanPath(ctx, path, info)
			} else {
				return
			}

			// If a backend returns interfaces.SkipDir, then
			// this is the signal that it doesn't need to
			// return any different information in a deeper
			// hierarchy of that scan. This is common when
			// we have a backend that makes a whole
			// repository or directory determination. In
			// this case, store this rejection so that we
			// don't wastefully call this backend again with
			// a child path. The backend will likely want to
			// also return a result with the SkipDir. It
			// must not return SkipDir in response to a non
			// directory path.
			if err == interfaces.SkipDir {
				// TODO: we could use a different mutex
				// but I'm lazy and it won't help much!
				obj.mu.Lock()
				obj.skipdirs[backend][info.UID] = struct{}{}
				obj.mu.Unlock()
			} else if err != nil {
				// XXX: ShutdownOnError and cancel the ctx?
				mu.Lock()
				errors = append(errors, err)
				mu.Unlock()
				return // goroutine ends
			}

			if result == nil { // skip nil results
				return
			}
			// tag (annotate) the result
			tagResultBackend(result, backend)

			// store results
			obj.mu.Lock()
			if _, exists := obj.results[info.UID]; !exists {
				obj.results[info.UID] = make(map[interfaces.Backend]*interfaces.Result)
			}
			if old, exists := obj.results[info.UID][backend]; exists {
				// If we get a duplicate result, this can happen
				// if there's a bug, or if we asked to scan the
				// same thing more than once. As a result, run a
				// cmp on both results, and if they're the same,
				// then we can safely ignore this issue.
				// XXX: can cached results cause this to fail?
				if err := old.Cmp(result); err != nil {
					e := errwrap.Wrapf(err, "duplicate result for path: %s", path)
					mu.Lock()
					errors = append(errors, e)
					mu.Unlock()
					return // goroutine ends
				}
			}
			obj.results[info.UID][backend] = result
			obj.mu.Unlock()

			// XXX: cache results
			//	if x, ok := backend.(interfaces.CachedDataBackend); ok {
			//		result, err = x.LookupData(ctx, data, info)
			//	} else if x, ok := backend.(interfaces.CachedPathBackend); ok {
			//		result, err = x.LookupPath(ctx, path, info)
			//	}

		}(backend)
	}
	wg.Wait()

	if len(errors) > 0 {
		var ea error
		for _, e := range errors {
			ea = errwrap.Append(ea, e)
		}
		return errwrap.Wrapf(ea, "scan func errored")
	}

	return nil
}

// Result returns the results after a Scan operation is run. It contains a Wait
// the blocks until all the Scan work has finished. To cancel and unblock this,
// cancel the context that was passed in to the Scan function. Do *not* call
// Result until the Recurse function finished or you will not necessarily get
// the correct results. For example, if all of the backends haven't started
// running, then you won't get the right value.
func (obj *Scanner) Result() (interfaces.ResultSet, error) {
	defer obj.wg.Wait()
	return obj.results, nil // TODO: should we pass the Recurse errors here?
}

func tagResultBackend(result *interfaces.Result, backend interfaces.Backend) {
	if result.Meta == nil {
		result.Meta = &interfaces.Meta{}
	}
	result.Meta.Backend = backend // tag it!
	if result.More == nil || len(result.More) == 0 {
		return
	}
	for _, x := range result.More {
		tagResultBackend(x, backend)
	}
}

func tagResultIterator(result *interfaces.Result, iterator interfaces.Iterator) {
	if result.Meta == nil {
		result.Meta = &interfaces.Meta{}
	}
	result.Meta.Iterator = iterator // tag it!
	if result.More == nil || len(result.More) == 0 {
		return
	}
	for _, x := range result.More {
		tagResultIterator(x, iterator)
	}
}
