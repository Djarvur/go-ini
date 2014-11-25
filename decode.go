// Decode INI files with a syntax similar to JSON decoding
package ini

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"reflect"
	"strconv"
	"strings"
)

// Unmarshal parses the INI-encoded data and stores the result
// in the value pointed to by v.
func Unmarshal(data []byte, v interface{}) error {
	var d decodeState
	d.init(data)
	return d.unmarshal(v)
}

type Unmatched struct {
	lineNum int
	line    string
}

func (u Unmatched) String() string {
	return fmt.Sprintf("%d %s", u.lineNum, u.line)
}

type IniError struct {
	lineNum  int
	line     string
	iniError string
}

// conform to Error Interfacer
func (e *IniError) Error() string {
	return fmt.Sprintf("%s - %d: %s", e.iniError, e.lineNum, e.line)
}

// decodeState represents the state while decoding a INI value.
type decodeState struct {
	lineNum    int
	line       string
	scanner    *bufio.Scanner
	savedError error
	unmatched  []Unmatched
}

type sectionTag struct {
	tag      string
	value    reflect.Value
	children map[string]sectionTag
}

func (t sectionTag) String() string {
	return fmt.Sprintf("<section %s>", t.tag)
}

func (d *decodeState) init(data []byte) *decodeState {

	d.lineNum = 0
	d.line = ""
	d.scanner = bufio.NewScanner(bytes.NewReader(data))
	d.savedError = nil

	return d
}

// error aborts the decoding by panicking with err.
func (d *decodeState) error(err error) {
	panic(err)
}

// saveError saves the first err it is called with,
// for reporting at the end of the unmarshal.
func (d *decodeState) saveError(err error) {
	if d.savedError == nil {
		d.savedError = err
	}
}

func (d *decodeState) generateMap(m map[string]sectionTag, v reflect.Value) {

	if v.Type().Kind() == reflect.Ptr {
		d.generateMap(m, v.Elem())
	} else if v.Kind() == reflect.Struct {
		typ := v.Type()
		for i := 0; i < typ.NumField(); i++ {

			sf := typ.Field(i)
			f := v.Field(i)

			tag := sf.Tag.Get("ini")
			if len(tag) == 0 {
				tag = sf.Name
			}
			tag = strings.TrimSpace(strings.ToLower(tag))

			st := sectionTag{tag, f, make(map[string]sectionTag)}

			// some structures are just for organizing data
			if tag != "-" {
				m[tag] = st
			}

			if f.Type().Kind() == reflect.Struct {
				if tag == "-" {
					d.generateMap(m, f)
				} else {
					// little namespacing here so property names can
					// be the same under different sections
					d.generateMap(st.children, f)
				}
			}
		}
	} else {
		d.saveError(&IniError{d.lineNum, d.line, fmt.Sprintf("Can't map into type %s", v.Kind())})
	}

}

func (d *decodeState) unmarshal(x interface{}) error {

	var parentMap map[string]sectionTag = make(map[string]sectionTag)

	d.generateMap(parentMap, reflect.ValueOf(x))

	var parentSection sectionTag
	var hasParent bool = false

	for d.scanner.Scan() {
		if d.savedError != nil {
			break
		}

		line := strings.TrimSpace(d.scanner.Text())
		d.lineNum++
		d.line = line

		//log.Printf("Scanned (%d): %s\n", lineNum, line)

		if len(line) < 1 || line[0] == ';' || line[0] == '#' {
			continue // skip comments
		}

		if line[0] == '[' && line[len(line)-1] == ']' {
			parentSection, hasParent = parentMap[strings.ToLower(line)]
			continue // in a section
		}

		matches := strings.SplitN(line, "=", 2)
		matched := false

		// potential property=value
		if len(matches) == 2 {
			n := strings.ToLower(strings.TrimSpace(matches[0]))
			s := strings.TrimSpace(matches[1])

			if hasParent {
				// "section" property
				childSection, hasChild := parentSection.children[n]
				if hasChild {
					d.setValue(childSection.value, s)
					matched = true
				}
			} else {
				// top level property
				propSection, hasProp := parentMap[n]
				if hasProp {
					d.setValue(propSection.value, s)
					matched = true
				}
			}
		}

		if !matched {
			d.unmatched = append(d.unmatched, Unmatched{d.lineNum, line})
		}
	}

	return d.savedError
}

// Set Value with given string
func (d *decodeState) setValue(v reflect.Value, s string) {
	//log.Printf("SET(%s, %s)", v.Kind(), s)

	switch v.Kind() {

	case reflect.String:
		v.SetString(s)

	case reflect.Bool:
		v.SetBool(boolValue(s))

	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil || v.OverflowInt(n) {
			panic(fmt.Sprintf("Invalid int '%s' specified on line %d", s, d.lineNum))
		}
		v.SetInt(n)

	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil || v.OverflowUint(n) {
			panic(fmt.Sprintf("Invalid uint '%s' specified on line %d", s, d.lineNum))
		}
		v.SetUint(n)

	case reflect.Float32, reflect.Float64:
		n, err := strconv.ParseFloat(s, v.Type().Bits())
		if err != nil || v.OverflowFloat(n) {
			panic(fmt.Sprintf("Invalid float '%s' specified on line %d", s, d.lineNum))
		}
		v.SetFloat(n)

	case reflect.Slice:

		d.sliceValue(v, s)

	default:
		d.saveError(&IniError{d.lineNum, d.line, fmt.Sprintf("Can't set value of type %s", v.Kind())})
	}

}

func (d *decodeState) sliceValue(v reflect.Value, s string) {

	switch v.Type().Elem().Kind() {
	case reflect.String:
		v.Set(reflect.Append(v, reflect.ValueOf(s)))

	case reflect.Bool:
		v.Set(reflect.Append(v, reflect.ValueOf(boolValue(s))))

	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		// Hardcoding of []int temporarily
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			panic(fmt.Sprintf("Invalid int '%s' specified on line %d", s, d.lineNum))
		}

		n1 := reflect.ValueOf(n)
		n2 := n1.Convert(v.Type().Elem())

		v.Set(reflect.Append(v, n2))

	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			panic(fmt.Sprintf("Invalid uint '%s' specified on line %d", s, d.lineNum))
		}

		n1 := reflect.ValueOf(n)
		n2 := n1.Convert(v.Type().Elem())

		v.Set(reflect.Append(v, n2))

	case reflect.Float32, reflect.Float64:
		n, err := strconv.ParseFloat(s, 64)
		if err != nil {
			panic(fmt.Sprintf("Invalid float32 '%s' specified on line %d", s, d.lineNum))
		}

		n1 := reflect.ValueOf(n)
		n2 := n1.Convert(v.Type().Elem())

		v.Set(reflect.Append(v, n2))

	default:
		d.saveError(&IniError{d.lineNum, d.line, fmt.Sprintf("Can't set value in array of type %s",
			v.Type().Elem().Kind())})
	}

}

// Returns true for truthy values like t/true/y/yes/1, false otherwise
func boolValue(s string) bool {
	v := false
	switch strings.ToLower(s) {
	case "t", "true", "y", "yes", "1":
		v = true
	}

	return v
}

// A Decoder reads and decodes INI object from an input stream.
type Decoder struct {
	r io.Reader
	d decodeState
}

// NewDecoder returns a new decoder that reads from r.
//
// The decoder introduces its own buffering and may
// read data from r beyond the JSON values requested.
func NewDecoder(r io.Reader) *Decoder {
	return &Decoder{r: r}
}

// Decode reads the INI file and stores it in the value pointed to by v.
//
// See the documentation for Unmarshal for details about
// the conversion of an INI into a Go value.
func (dec *Decoder) Decode(v interface{}) error {

	buf, readErr := ioutil.ReadAll(dec.r)
	if readErr != nil {
		return readErr
	}
	// Don't save err from unmarshal into dec.err:
	// the connection is still usable since we read a complete JSON
	// object from it before the error happened.
	dec.d.init(buf)
	err := dec.d.unmarshal(v)

	return err
}

// UnparsedLines returns an array of strings where each string is an
// unparsed line from the file.
func (dec *Decoder) Unmatched() []Unmatched {
	return dec.d.unmatched
}
