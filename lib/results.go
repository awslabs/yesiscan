package lib

import (
	"fmt"
	"sort"

	"github.com/purpleidea/yesiscan/interfaces"
	"github.com/purpleidea/yesiscan/util"
	"github.com/purpleidea/yesiscan/util/licenses"
)

const (
	debug = false
)

type AnnotatedBackend struct {
	Backend          interfaces.Backend
	Weight           float64
	ScaledConfidence float64
}

type SortedBackends []*AnnotatedBackend

func (obj SortedBackends) Len() int      { return len(obj) }
func (obj SortedBackends) Swap(i, j int) { obj[i], obj[j] = obj[j], obj[i] }

//func (obj SortedBackends) Less(i, j int) bool { return obj[i].Weight < obj[j].Weight }
func (obj SortedBackends) Less(i, j int) bool {
	return obj[i].ScaledConfidence < obj[j].ScaledConfidence
}

//func (obj SortedBackends) Sort()     { sort.Sort(obj) }

// SimpleResults is a simple way to format the results. This is the first
// display function created and is mostly used for debugging and initial POC.
func SimpleResults(results interfaces.ResultSet, backendWeights map[interfaces.Backend]float64) (string, error) {
	if len(results) == 0 {
		return "", fmt.Errorf("no results obtained")
	}

	str := ""
	// XXX: handle dir's in here specially and merge in their weights with child paths!
	for uri, m := range results { // FIXME: sort and process properly
		bs := []*AnnotatedBackend{}
		ttl := 0.0 // total weight for the set of backends at this uri
		for backend := range m {
			weight, exists := backendWeights[backend]
			if !exists {
				return "", fmt.Errorf("no weight found for backend: %s", backend.String())
			}
			b := &AnnotatedBackend{
				Backend: backend,
				Weight:  weight,
			}
			bs = append(bs, b)
			ttl += weight
		}
		f := 0.0 // NOTE: confidence *if* the different results agree!
		//for backend, result := range m {
		for _, b := range bs { // for backend, result := range m
			backend := b.Backend
			weight := b.Weight // backendWeights[backend]
			result := m[backend]
			scale := weight / ttl
			b.ScaledConfidence = result.Confidence * scale
			f = f + b.ScaledConfidence
		}

		sort.Sort(sort.Reverse(SortedBackends(bs)))
		display := uri // show the URI
		smartURI := util.SmartURI(uri)
		hyperlink := util.ShellHyperlinkEncode(display, smartURI)
		str += fmt.Sprintf("%s (%.2f%%)\n", hyperlink, f*100.0)
		for _, b := range bs { // for backend, result := range m
			backend := b.Backend
			weight := b.Weight // backendWeights[backend]
			result := m[backend]
			l := licenses.Join(result.Licenses)
			str += fmt.Sprintf("    %s (%.2f/%.2f)  %s (%.2f%%)\n", backend.String(), weight, ttl, l, result.Confidence*100.0)
			if !debug {
				continue
			}
			it := result.Meta.Iterator // at least one must be present
			for {
				str += fmt.Sprintf("        %s\n", it)
				newIt := it.GetIterator()
				if newIt == nil {
					break
				}
				it = newIt
			}
			if parser := it.GetParser(); parser != nil {
				str += fmt.Sprintf("            %s\n", parser)
			}
		}
	}
	return str, nil
}
