//declscope:namespace sqlite

package sqlite

import (
	"bytes"
	"compress/flate"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"

	"github.com/Akkanbe/gotwamp/internal/sender"
	"github.com/Akkanbe/gotwamp/internal/store"
)

// The payload of a result row (results.data) is the JSON of its Summary or
// RunError in one of two formats, told apart by the first octet:
//
//   - '{': the JSON itself (schema version 1).
//   - payloadDeflate: the compact JSON, without the fields whose value is
//     their type's zero value, compressed with DEFLATE and dictionary 1.
//     It takes about 350 octets for a summary where the JSON takes 2 KiB.
//
// Leaving out zero values loses nothing: decoding into a zero value leaves
// the missing fields zero. The dictionary is part of the format and never
// changes; a new dictionary would be a new format octet.
const payloadDeflate = 0x01

// compactPayload writes the payloads of the writer. It keeps its DEFLATE
// state between rows, so it belongs to the writer goroutine alone.
type compactPayload struct {
	buf bytes.Buffer
	fw  *flate.Writer
}

func newCompactPayload() *compactPayload {
	c := &compactPayload{}
	// flate.NewWriterDict fails only for an invalid level.
	c.fw, _ = flate.NewWriterDict(&c.buf, flate.DefaultCompression, []byte(dictionary1))
	return c
}

// encode returns the payload of v, a *sender.Summary or a *store.RunError,
// in the compressed format.
func (c *compactPayload) encode(v any) ([]byte, error) {
	b, err := compactJSON(v)
	if err != nil {
		return nil, err
	}
	c.buf.Reset()
	c.buf.WriteByte(payloadDeflate)
	c.fw.Reset(&c.buf)
	if _, err := c.fw.Write(b); err != nil {
		return nil, err
	}
	if err := c.fw.Close(); err != nil {
		return nil, err
	}
	return bytes.Clone(c.buf.Bytes()), nil
}

// zeroTrees are the decoded JSON of the zero values of the payload types:
// what compactJSON leaves out.
var zeroTrees = map[reflect.Type]any{
	reflect.TypeFor[*sender.Summary](): mustTree(&sender.Summary{}),
	reflect.TypeFor[*store.RunError](): mustTree(&store.RunError{}),
}

// compactJSON returns the JSON of v without the fields equal to those of
// v's zero value.
func compactJSON(v any) ([]byte, error) {
	zero, ok := zeroTrees[reflect.TypeOf(v)]
	if !ok {
		return nil, fmt.Errorf("no compact form for %T", v)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	tree, err := decodeTree(b)
	if err != nil {
		return nil, err
	}
	tree, _ = prune(tree, zero)
	return json.Marshal(tree)
}

// prune drops from v what equals zero, recursively in objects, and reports
// whether anything is left.
func prune(v, zero any) (any, bool) {
	vm, vok := v.(map[string]any)
	zm, zok := zero.(map[string]any)
	if vok && zok {
		for k, x := range vm {
			if kept, keep := prune(x, zm[k]); keep {
				vm[k] = kept
			} else {
				delete(vm, k)
			}
		}
		return vm, len(vm) > 0
	}
	return v, !reflect.DeepEqual(v, zero)
}

// decodeTree decodes JSON with its numbers as written, so that re-encoding
// keeps every digit.
func decodeTree(b []byte) (any, error) {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var tree any
	if err := d.Decode(&tree); err != nil {
		return nil, err
	}
	return tree, nil
}

func mustTree(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	tree, err := decodeTree(b)
	if err != nil {
		panic(err)
	}
	return tree
}

// payloadJSON returns the JSON of a payload in either format.
func payloadJSON(data []byte) ([]byte, error) {
	switch {
	case len(data) == 0:
		return nil, errors.New("empty payload")
	case data[0] == '{':
		return data, nil
	case data[0] == payloadDeflate:
		r := flate.NewReaderDict(bytes.NewReader(data[1:]), []byte(dictionary1))
		defer r.Close()
		return io.ReadAll(r)
	default:
		return nil, fmt.Errorf("unknown payload format %#x", data[0])
	}
}

// dictionary1 is the DEFLATE dictionary of format payloadDeflate: the
// compact JSON of a summary and of a run error with every field set, which
// holds the field names in the order compactJSON writes them. Never change
// it: rows written with it would no longer decode.
const dictionary1 = `{"Aborted":true,"DSCPReflected":{"CE":51,"Count":51,"Mode":51,"Remarked":51},"DupForward":51,"DupRev` +
	`erse":51,"EndedAt":"2026-10-05T01:26:14.870342795Z","ForeignSource":51,"Forward":{"Count":51,"Max":1` +
	`4857,"Mean":14857,"Median":14857,"Min":14857,"P90":14857,"P95":14857,"P99":14857,"P999":14857,"Sampl` +
	`es":51,"StdDev":14857},"ForwardHops":{"Count":51,"Max":51,"Min":51,"Mode":51},"Invalid":true,"Invali` +
	`dRTT":51,"Jitter":{"IPDVForward":{"Count":51,"Max":14857,"Mean":14857,"Median":14857,"Min":14857,"P9` +
	`0":14857,"P95":14857,"P99":14857,"P999":14857,"Samples":51,"StdDev":14857},"IPDVRTT":{"Count":51,"Ma` +
	`x":14857,"Mean":14857,"Median":14857,"Min":14857,"P90":14857,"P95":14857,"P99":14857,"P999":14857,"S` +
	`amples":51,"StdDev":14857},"IPDVReverse":{"Count":51,"Max":14857,"Mean":14857,"Median":14857,"Min":1` +
	`4857,"P90":14857,"P95":14857,"P99":14857,"P999":14857,"Samples":51,"StdDev":14857},"P95P50":14857,"P` +
	`DV":{"Count":51,"Max":14857,"Mean":14857,"Median":14857,"Min":14857,"P90":14857,"P95":14857,"P99":14` +
	`857,"P999":14857,"Samples":51,"StdDev":14857},"RFC3550":14857},"Late":51,"Local":"192.0.2.1:50000","` +
	`LossRatio":0.02,"LossSplitValid":true,"Lost":51,"LostForward":51,"LostReverse":51,"Malformed":51,"Ma` +
	`xClockError":14857,"MaxClockErrorUnknown":true,"NegativeOWD":51,"OWDValid":true,"Pending":51,"RTT":{` +
	`"Count":51,"Max":14857,"Mean":14857,"Median":14857,"Min":14857,"P90":14857,"P95":14857,"P99":14857,"` +
	`P999":14857,"Samples":51,"StdDev":14857},"Received":51,"ReflectedMismatch":51,"ReflectorDelay":{"Cou` +
	`nt":51,"Max":14857,"Mean":14857,"Median":14857,"Min":14857,"P90":14857,"P95":14857,"P99":14857,"P999` +
	`":14857,"Samples":51,"StdDev":14857},"ReflectorSynced":true,"Remote":"192.0.2.1:50000","Reordered":5` +
	`1,"ReorderedReverse":51,"Reverse":{"Count":51,"Max":14857,"Mean":14857,"Median":14857,"Min":14857,"P` +
	`90":14857,"P95":14857,"P99":14857,"P999":14857,"Samples":51,"StdDev":14857},"ReverseHops":{"Count":5` +
	`1,"Max":51,"Min":51,"Mode":51},"SID":"33333333333333333333333333333333","SendErrors":51,"SenderSynce` +
	`d":true,"Sent":51,"StartedAt":"2026-10-05T01:26:14.870342795Z","Stray":51,"Timestamping":51,"TxInval` +
	`id":51,"TxMissing":51,"TxTimestamps":true,"Unsupported":51,"Window":51}{"Accept":3,"Message":"accept` +
	`-session accept=3","Stage":"request"}`
